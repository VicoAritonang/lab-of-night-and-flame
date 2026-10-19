import { useMemo } from "react";

/**
 * The sky behind the Lab — decorative only (aria-hidden, no pointer events).
 *
 *   Night: three twinkling star fields, glintstone sparkles, a crescent moon with a breathing halo,
 *          and the occasional shooting star.
 *   Flame: a sun with slowly turning rays, embers rising from the bottom edge, and a heat haze.
 *
 * Both layers are rendered once; CSS shows the one matching the active theme and the hidden one is
 * `display: none`, so its animations cost nothing. Positions come from a seeded PRNG, so the sky is
 * identical on every load (and in screenshots). `prefers-reduced-motion` freezes everything.
 */
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const FIELD_W = 2600, FIELD_H = 1600;

function starField(seed: number, n: number, alpha: [number, number]): string {
  const r = rng(seed);
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    const x = Math.round(r() * FIELD_W), y = Math.round(r() * FIELD_H);
    const a = (alpha[0] + r() * (alpha[1] - alpha[0])).toFixed(2);
    const warm = r() < 0.12; // a few gold-white stars among the blue-white ones
    out.push(`${x}px ${y}px 0 0 rgba(${warm ? "255, 236, 200" : "214, 226, 255"}, ${a})`);
  }
  return out.join(", ");
}

export function Sky() {
  const night = useMemo(() => {
    const r = rng(7);
    return {
      far: starField(11, 170, [0.25, 0.6]),
      mid: starField(23, 70, [0.45, 0.85]),
      near: starField(37, 24, [0.7, 1]),
      sparkles: Array.from({ length: 8 }, () => ({
        left: `${4 + r() * 92}%`, top: `${6 + r() * 70}%`, d: `${(r() * -9).toFixed(2)}s`, t: `${(5 + r() * 6).toFixed(2)}s`, s: (0.6 + r() * 0.8).toFixed(2),
      })),
    };
  }, []);
  const embers = useMemo(() => {
    const r = rng(1312);
    return Array.from({ length: 26 }, () => ({
      left: `${(r() * 100).toFixed(2)}%`,
      size: `${(1.6 + r() * 2.8).toFixed(1)}px`,
      dur: `${(9 + r() * 10).toFixed(2)}s`,
      delay: `${(r() * -18).toFixed(2)}s`,
      drift: `${Math.round(-60 + r() * 120)}px`,
      hue: r() < 0.35 ? "var(--ember-gold)" : "var(--ember-hot)",
    }));
  }, []);

  return (
    <div className="sky" aria-hidden="true">
      <div className="sky-night">
        <i className="nebula" />
        <i className="stars far" style={{ boxShadow: night.far }} />
        <i className="stars mid" style={{ boxShadow: night.mid }} />
        <i className="stars near" style={{ boxShadow: night.near }} />
        {night.sparkles.map((s, i) => (
          <i key={i} className="glint" style={{ left: s.left, top: s.top, ["--d" as any]: s.d, ["--t" as any]: s.t, ["--s" as any]: s.s }} />
        ))}
        <i className="shooting-star" />
        <svg className="moon" viewBox="0 0 200 200">
          <defs>
            <radialGradient id="moon-halo" cx="50%" cy="50%" r="50%">
              <stop offset="35%" stopColor="rgba(170, 195, 255, 0.28)" />
              <stop offset="100%" stopColor="rgba(170, 195, 255, 0)" />
            </radialGradient>
            <radialGradient id="moon-face" cx="66%" cy="32%" r="70%">
              <stop offset="0%" stopColor="#f4f6ff" />
              <stop offset="60%" stopColor="#d5ddf5" />
              <stop offset="100%" stopColor="#9fb0dd" />
            </radialGradient>
            <mask id="moon-cut">
              <rect width="200" height="200" fill="#fff" />
              <circle cx="76" cy="118" r="50" fill="#000" />
            </mask>
          </defs>
          <circle className="moon-halo" cx="100" cy="100" r="98" fill="url(#moon-halo)" />
          <g mask="url(#moon-cut)">
            <circle cx="100" cy="100" r="56" fill="url(#moon-face)" />
            <circle cx="130" cy="84" r="7" fill="rgba(120, 136, 190, 0.35)" />
            <circle cx="112" cy="62" r="4.5" fill="rgba(120, 136, 190, 0.3)" />
            <circle cx="145" cy="112" r="3.5" fill="rgba(120, 136, 190, 0.3)" />
          </g>
        </svg>
      </div>

      <div className="sky-flame">
        <i className="heat" />
        <div className="sun">
          <i className="sun-rays" />
          <i className="sun-corona" />
          <i className="sun-disc" />
        </div>
        {embers.map((e, i) => (
          <i key={i} className="ember" style={{ left: e.left, width: e.size, height: e.size, background: e.hue, ["--dur" as any]: e.dur, ["--delay" as any]: e.delay, ["--drift" as any]: e.drift }} />
        ))}
      </div>
    </div>
  );
}
