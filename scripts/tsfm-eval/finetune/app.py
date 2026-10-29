"""Fine-tuning front for Chronos-2, run inside the ready-made kingfs/unitshub chronos image (it already carries torch (CPU),
chronos-forecasting and the weights, so nothing is downloaded or built):

    docker run -d --name ut-ft -p 18010:8000 -v <this directory>:/srv/ft --entrypoint uvicorn \
        kingfs/unitshub:chronos-0.4.0 --app-dir /srv/ft app:app --host 0.0.0.0 --port 8000

This is the "finetune" protocol of the platform's model_service algorithm. The platform decides how to fine-tune (mode,
steps, learning rate, batch size, context length) and sends those values with every fit; this service trains with exactly
what it is sent.

POST /v1/fit          {model_key, values, horizon, mode, steps, learning_rate, batch_size, context_length}
                      fine-tunes a copy of the pretrained model on the series and keeps it under model_key
GET  /v1/tuned?key=   when the copy of that key was made (404 when there is none)
POST /v1/predict      {model_key, values, horizon, quantiles}: from the copy of model_key, else the pretrained model
GET  /healthz

A fit does not block predictions: it trains a copy, and only swapping the finished copy in takes the (short) lock. Tuned
copies live in memory only (the newest KEEP of them); after a restart the platform finds none and submits the series again.
If API_KEY is set in the environment, requests need "Authorization: Bearer <API_KEY>".
"""
from __future__ import annotations

import collections
import datetime
import importlib.util
import os
import threading
import time
from typing import List, Optional

import torch
from chronos import BaseChronosPipeline
from fastapi import Depends, FastAPI, Header, HTTPException
from pydantic import BaseModel, Field

MODEL_DIR = os.environ.get("MODEL_DIR", "/app/models/chronos")
KEEP = int(os.environ.get("KEEP_TUNED", "8"))  # each tuned copy is a full copy of the model
API_KEY = os.environ.get("API_KEY", "")

SWAP = threading.Lock()  # guards TUNED
FIT = threading.Lock()  # one fit at a time: torch already uses every core
PREDICT = threading.Lock()  # one forward pass at a time
BASE = BaseChronosPipeline.from_pretrained(MODEL_DIR, device_map="cpu")
TUNED: "collections.OrderedDict[str, dict]" = collections.OrderedDict()


def auth(authorization: Optional[str] = Header(None)):
    if API_KEY and authorization != f"Bearer {API_KEY}":
        raise HTTPException(401, "bad or missing key")


app = FastAPI(title="chronos-2 fine-tuning", dependencies=[Depends(auth)])


class FitRequest(BaseModel):
    model_key: str
    values: List[float]
    horizon: int = Field(..., ge=1, le=64)
    mode: str = Field("full", pattern="^(full|lora)$")
    steps: int = Field(30, ge=1, le=2000)
    learning_rate: float = Field(1e-5, gt=0, le=1)
    batch_size: int = Field(16, ge=1, le=256)
    context_length: int = Field(256, ge=16, le=8192)


class PredictRequest(BaseModel):
    model_key: Optional[str] = None
    values: List[float]
    horizon: int = Field(..., ge=1, le=64)
    quantiles: List[float] = Field(default_factory=lambda: [0.5, 0.9])


@app.get("/healthz")
def healthz():
    with SWAP:
        return {"ok": True, "tuned": list(TUNED)}


@app.get("/v1/tuned")
def tuned(key: str):
    with SWAP:
        entry = TUNED.get(key)
    if entry is None:
        raise HTTPException(404, "no fine-tuned copy for this key")
    return {k: v for k, v in entry.items() if k != "pipeline"}


@app.post("/v1/fit")
def fit(req: FitRequest):
    if len(req.values) < req.horizon * 4:
        raise HTTPException(400, "the series is too short to fine-tune on")
    if req.mode == "lora" and importlib.util.find_spec("peft") is None:
        # the library would quietly fall back to full fine-tuning; a record that says lora must mean lora
        raise HTTPException(400, "lora fine-tuning needs the peft package, which this image does not carry")
    with FIT:
        started = time.time()  # after the wait for the other fits: this is the time this fit trained
        try:
            pipeline = BASE.fit(
                inputs=[torch.tensor(req.values, dtype=torch.float32)],
                prediction_length=req.horizon,
                finetune_mode=req.mode,
                learning_rate=req.learning_rate,
                num_steps=req.steps,
                batch_size=req.batch_size,
                context_length=req.context_length,
                remove_printer_callback=True,
                report_to="none",
            )
        except ImportError as exc:
            raise HTTPException(400, f"{req.mode} fine-tuning is not available here: {exc}")
    seconds = round(time.time() - started, 3)
    entry = {
        "pipeline": pipeline,
        "fitted_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "points": len(req.values),
        "steps": req.steps,
        "mode": req.mode,
        "learning_rate": req.learning_rate,
        "batch_size": req.batch_size,
        "context_length": req.context_length,
        "horizon": req.horizon,
        "seconds": seconds,
    }
    with SWAP:
        TUNED[req.model_key] = entry
        TUNED.move_to_end(req.model_key)
        while len(TUNED) > KEEP:
            TUNED.popitem(last=False)
    return {k: v for k, v in entry.items() if k != "pipeline"}


@app.post("/v1/predict")
def predict(req: PredictRequest):
    if len(req.values) < 8:
        raise HTTPException(400, "at least 8 history points are needed")
    started = time.time()
    levels = sorted(set(req.quantiles) | {0.5})
    with SWAP:
        entry = TUNED.get(req.model_key or "")
    pipe = entry["pipeline"] if entry else BASE
    with PREDICT:
        quantiles, mean = pipe.predict_quantiles(
            [torch.tensor(req.values, dtype=torch.float32)], prediction_length=req.horizon, quantile_levels=levels
        )
    q = quantiles[0][0].numpy()  # [variates, horizon, levels] -> [horizon, levels]
    return {
        "tuned": entry is not None,
        "mean": mean[0].numpy().reshape(-1).tolist(),
        "quantiles": {str(l): q[:, i].tolist() for i, l in enumerate(levels)},
        "seconds": round(time.time() - started, 3),
    }
