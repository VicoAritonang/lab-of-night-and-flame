import { useMemo } from "react";

/** Word-level diff for comparing two outputs. */
export function Diff({ a, b }: { a: string; b: string }) {
  const parts = useMemo(() => {
    const x = a.split(/(\s+)/).slice(0, 800), y = b.split(/(\s+)/).slice(0, 800);
    const dp = Array.from({ length: x.length + 1 }, () => new Uint16Array(y.length + 1));
    for (let i = x.length - 1; i >= 0; i--) for (let j = y.length - 1; j >= 0; j--) dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    const out: { t: string; k: "same" | "del" | "ins" }[] = [];
    let i = 0, j = 0;
    while (i < x.length && j < y.length) {
      if (x[i] === y[j]) { out.push({ t: x[i], k: "same" }); i++; j++; } else if (dp[i + 1][j] >= dp[i][j + 1]) out.push({ t: x[i++], k: "del" }); else out.push({ t: y[j++], k: "ins" });
    }
    while (i < x.length) out.push({ t: x[i++], k: "del" });
    while (j < y.length) out.push({ t: y[j++], k: "ins" });
    return out;
  }, [a, b]);
  return <div className="al-text wb-diff">{parts.map((p, i) => p.k === "same" ? <span key={i}>{p.t}</span> : <span key={i} className={p.k === "del" ? "diff-del" : "diff-ins"}>{p.t}</span>)}</div>;
}
