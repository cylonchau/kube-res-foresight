"""Fine-tuning front for Chronos-2, run inside the ready-made kingfs/unitshub chronos image (it already carries torch (CPU),
chronos-forecasting and the weights, so nothing is downloaded or built):

    docker run -d --name ut-ft -p 18010:8000 -v <this directory>:/srv/ft --entrypoint uvicorn \
        kingfs/unitshub:chronos-0.4.0 --app-dir /srv/ft app:app --host 0.0.0.0 --port 8000

POST /v1/fit      submit a series: a copy of the model is fine-tuned on it (full fine-tuning, no LoRA: peft is not installed)
POST /v1/predict  the next steps, from the copy tuned on series_id, or from the pretrained model when there is none
GET  /healthz

Work package A9 of the platform's algorithm plan; an evaluation harness, not a production service.
"""
from __future__ import annotations

import collections
import threading
import time
from typing import Dict, List, Optional

import torch
from chronos import BaseChronosPipeline
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

MODEL_DIR = "/app/models/chronos"
KEEP = 4  # tuned copies kept; each is a full copy of the model, so older ones are dropped

LOCK = threading.Lock()  # one fit or predict at a time: torch already uses every core
BASE = BaseChronosPipeline.from_pretrained(MODEL_DIR, device_map="cpu")
TUNED: "collections.OrderedDict[str, object]" = collections.OrderedDict()
app = FastAPI(title="chronos-2 fine-tuning")


class FitRequest(BaseModel):
    series_id: str
    values: List[float]
    horizon: int = Field(..., ge=1, le=64)
    steps: int = Field(30, ge=1, le=2000)
    learning_rate: float = 1e-5
    batch_size: int = Field(16, ge=1, le=256)
    context_length: Optional[int] = Field(256, ge=16, le=8192)


class PredictRequest(BaseModel):
    series_id: Optional[str] = None
    values: List[float]
    horizon: int = Field(..., ge=1, le=64)
    quantiles: List[float] = Field(default_factory=lambda: [0.5, 0.9])


@app.get("/healthz")
def healthz():
    return {"ok": True, "tuned": list(TUNED)}


@app.post("/v1/fit")
def fit(req: FitRequest):
    if len(req.values) < req.horizon * 4:
        raise HTTPException(400, "the series is too short to fine-tune on")
    started = time.time()
    with LOCK:
        # the model is trained to predict `horizon` steps from windows cut out of the submitted series
        tuned = BASE.fit(
            inputs=[torch.tensor(req.values, dtype=torch.float32)],
            prediction_length=req.horizon,
            finetune_mode="full",
            learning_rate=req.learning_rate,
            num_steps=req.steps,
            batch_size=req.batch_size,
            context_length=req.context_length,
            remove_printer_callback=True,
            report_to="none",
        )
        TUNED[req.series_id] = tuned
        TUNED.move_to_end(req.series_id)
        while len(TUNED) > KEEP:
            TUNED.popitem(last=False)
    return {"steps": req.steps, "seconds": round(time.time() - started, 3)}


@app.post("/v1/predict")
def predict(req: PredictRequest):
    if len(req.values) < 8:
        raise HTTPException(400, "at least 8 history points are needed")
    started = time.time()
    levels = sorted(set(req.quantiles) | {0.5})
    with LOCK:
        pipe = TUNED.get(req.series_id or "", BASE)
        quantiles, mean = pipe.predict_quantiles(
            [torch.tensor(req.values, dtype=torch.float32)], prediction_length=req.horizon, quantile_levels=levels
        )
    q = quantiles[0][0].numpy()  # [variates, horizon, levels] -> [horizon, levels]
    return {
        "tuned": (req.series_id or "") in TUNED,
        "mean": mean[0].numpy().reshape(-1).tolist(),
        "quantiles": {str(l): q[:, i].tolist() for i, l in enumerate(levels)},
        "seconds": round(time.time() - started, 3),
    }
