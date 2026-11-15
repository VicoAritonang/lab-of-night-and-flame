import { FEATURES } from "../site";

/** Public information: also rendered to static HTML during the build, with no API dependency. */
export default function About() {
  return (
    <article className="page" lang="id">
      <div className="page-head">
        <div className="grow">
          <div className="eyebrow">Vicoworks · Vico Aritonang</div>
          <h1 className="page-title">Lab Vico Aritonang</h1>
          <p className="page-sub">Lab Vicoworks, dengan nama antarmuka <span lang="en">Lab of Night and Flame</span>, adalah laboratorium AI karya Vico Aritonang untuk mengukur keandalan model dan memahami perilaku AI agent berdasarkan bukti eksperimen.</p>
        </div>
        <a className="btn primary" href="/">Open the bench</a>
      </div>
      <div className="grid g2">
        {FEATURES.map((feature) => (
          <section key={feature.id} id={feature.id} className="card">
            <div className="card-head"><h2 className="card-title">{feature.title}</h2></div>
            <div className="card-body">
              <p className="about-copy">{feature.text}</p>
              <a href={feature.href}>Buka workspace →</a>
            </div>
          </section>
        ))}
        <section className="card">
          <div className="card-head"><h2 className="card-title">Cara menggunakan Lab</h2></div>
          <div className="card-body">
            <ol className="about-steps"><li>Pilih atau impor dataset dan tentukan model.</li><li>Tetapkan scorer, protokol, anggaran, dan kriteria penerimaan.</li><li>Jalankan eksperimen, lalu periksa trial, statistik, dan laporan.</li><li>Untuk mengamati agent sendiri, hubungkan telemetry service ke Agent lab.</li></ol>
            <p className="muted">Workspace dapat memerlukan autentikasi pemilik. Dataset, trace, hasil eksperimen, dan API bukan dokumentasi publik. Demo simulasi diberi label terpisah dari pengukuran model nyata.</p>
            <a href="/llms.txt">Panduan teks untuk AI</a>
          </div>
        </section>
      </div>
    </article>
  );
}
