// Shared ember pixel-flame builder for the store-asset templates.
const FLAME = [
  "00011000",
  "00111100",
  "00111100",
  "01122110",
  "01122110",
  "11222211",
  "01122110",
  "00111100",
];
const STOPS = [
  [0.0, [255, 206, 107]],
  [0.55, [242, 121, 43]],
  [1.0, [207, 74, 31]],
];
function ember(t) {
  for (let i = 1; i < STOPS.length; i++) {
    if (t <= STOPS[i][0]) {
      const [t0, c0] = STOPS[i - 1];
      const [t1, c1] = STOPS[i];
      const k = (t - t0) / (t1 - t0);
      return c0.map((v, j) => Math.round(v + (c1[j] - v) * k));
    }
  }
  return STOPS[STOPS.length - 1][1];
}
function buildFlame(el, cell, gap) {
  el.style.display = "inline-grid";
  el.style.gridTemplateColumns = `repeat(8, ${cell}px)`;
  el.style.gap = `${gap}px`;
  el.style.lineHeight = "0";
  FLAME.forEach((row, r) => {
    row.split("").forEach((ch) => {
      const s = document.createElement("span");
      s.style.width = cell + "px";
      s.style.height = cell + "px";
      s.style.borderRadius = Math.max(0.5, cell * 0.12) + "px";
      if (ch !== "0") {
        const lift = ch === "2" ? 28 : 0;
        const [R, G, B] = ember(r / (FLAME.length - 1));
        s.style.background = `rgb(${Math.min(255, R + lift)},${Math.min(
          255,
          G + lift
        )},${Math.min(255, B + lift)})`;
      } else {
        s.style.background = "transparent";
      }
      el.appendChild(s);
    });
  });
}
