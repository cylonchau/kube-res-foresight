'use strict';
document.querySelectorAll('[data-diagram]').forEach(figure => {
  const viewport = figure.querySelector('.diagram-viewport');
  const canvas = figure.querySelector('.diagram-canvas');
  const output = figure.querySelector('output');
  const smaller = figure.querySelector('[data-diagram-action="out"]');
  const larger = figure.querySelector('[data-diagram-action="in"]');
  const full = figure.querySelector('[data-diagram-action="fullscreen"]');
  let zoom = 1;
  const apply = () => {
    canvas.style.width = `${zoom * 100}%`;
    figure.toggleAttribute('data-zoomed', zoom > 1);
    output.textContent = `${Math.round(zoom * 100)}%`;
    smaller.disabled = zoom <= 1;
    larger.disabled = zoom >= 4;
  };
  figure.dataset.interactive = 'true';
  if (!document.fullscreenEnabled) full.hidden = true;
  figure.addEventListener('click', async event => {
    const button = event.target.closest('[data-diagram-action]');
    if (!button) return;
    const action = button.dataset.diagramAction;
    if (action === 'in') zoom = Math.min(4, zoom + 0.5);
    if (action === 'out') zoom = Math.max(1, zoom - 0.5);
    if (action === 'fit') { zoom = 1; viewport.scrollTo(0, 0); }
    if (action === 'fullscreen') {
      try {
        if (document.fullscreenElement === figure) await document.exitFullscreen();
        else await figure.requestFullscreen();
      } catch { viewport.focus(); }
    }
    apply();
  });
  apply();
});
