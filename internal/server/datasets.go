package server

import (
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/dataset"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
	"io"
	"net/http"
	"strings"
)

func (s *Server) datasetRoutes() {
	s.route("GET /api/datasets", listDatasets)
	s.route("GET /api/datasets/{id}", datasetDetail)
	s.route("GET /api/datasets/{id}/samples", datasetSamples)
	s.route("POST /api/datasets/import", importDataset)
	s.route("POST /api/datasets/{id}/predictions", importPredictions)
	s.route("DELETE /api/datasets/{id}", deleteDataset)
	s.route("POST /api/artifacts", uploadArtifact)
	s.route("GET /api/artifacts/{digest}", getArtifact)
}
func listDatasets(r *Request) (any, error) {
	n, err := r.DB.Count(r.R.Context(), "datasets", nil)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		if _, err = r.Async("datasets.seed", M{}, "seed:v2"); err != nil {
			return nil, err
		}
	}
	rows, err := r.DB.List(r.R.Context(), "datasets", nil, 10000)
	if err != nil {
		return nil, err
	}
	store.Order(rows, "name", false)
	return rows, nil
}
func datasetDetail(r *Request) (any, error) {
	id := r.R.PathValue("id")
	ds, err := r.DB.Get(r.R.Context(), "datasets", id)
	if err != nil {
		return nil, err
	}
	var used int
	err = r.DB.Pool.QueryRow(r.R.Context(), "SELECT count(*) FROM arl.documents WHERE project_id=$1 AND kind='campaigns' AND data->>'dataset_id'=$2", r.DB.Project, id).Scan(&used)
	if err != nil {
		return nil, err
	}
	ds["used_by_campaigns"] = used
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT data->>'import_name',count(*) FROM arl.documents WHERE project_id=$1 AND kind='predictions' AND data->>'dataset_id'=$2 GROUP BY 1", r.DB.Project, id)
	if err != nil {
		return nil, err
	}
	preds := []M{}
	for rows.Next() {
		var name string
		var n int
		if err = rows.Scan(&name, &n); err != nil {
			rows.Close()
			return nil, err
		}
		preds = append(preds, M{"import_name": name, "n": n})
	}
	rows.Close()
	ds["predictions"] = preds
	rows, err = r.DB.Pool.Query(r.R.Context(), "SELECT DISTINCT jsonb_object_keys(COALESCE(data->'meta','{}'::jsonb)) FROM arl.documents WHERE project_id=$1 AND kind='samples' AND data->>'dataset_id'=$2 LIMIT 50", r.DB.Project, id)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for rows.Next() {
		var k string
		if err = rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	ds["meta_keys"] = keys
	return ds, nil
}
func datasetSamples(r *Request) (any, error) {
	id := r.R.PathValue("id")
	if _, err := r.DB.Get(r.R.Context(), "datasets", id); err != nil {
		return nil, err
	}
	offset, limit := queryInt(r.R, "offset", 0, 0, 50000), queryInt(r.R, "limit", 50, 1, 200)
	split, q := r.R.URL.Query().Get("split"), r.R.URL.Query().Get("q")
	where := "project_id=$1 AND kind='samples' AND data->>'dataset_id'=$2 AND ($3='' OR data->>'split'=$3) AND ($4='' OR data::text ILIKE '%'||$4||'%')"
	var total int
	if err := r.DB.Pool.QueryRow(r.R.Context(), "SELECT count(*) FROM arl.documents WHERE "+where, r.DB.Project, id, split, q).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := r.DB.Pool.Query(r.R.Context(), "SELECT data FROM arl.documents WHERE "+where+" ORDER BY (data->>'ordinal')::integer LIMIT $5 OFFSET $6", r.DB.Project, id, split, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []M{}
	for rows.Next() {
		var m M
		if err = rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return M{"items": out, "total": total, "offset": offset, "limit": limit}, rows.Err()
}
func multipart(r *Request) (func(), error) {
	select {
	case r.S.uploadSlots <- struct{}{}:
	case <-r.R.Context().Done():
		return nil, r.R.Context().Err()
	}
	r.R.Body = http.MaxBytesReader(r.W, r.R.Body, 128<<20)
	if err := r.R.ParseMultipartForm(1 << 20); err != nil {
		<-r.S.uploadSlots
		return nil, invalid("invalid multipart body or upload exceeds 128 MiB")
	}
	return func() { _ = r.R.MultipartForm.RemoveAll(); <-r.S.uploadSlots }, nil
}
func uploadField(r *Request, key string, max int64) (string, error) {
	f, h, err := r.R.FormFile(key)
	if err != nil {
		return "", invalid("missing file field: " + key)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(body)) > max {
		return "", &HTTPError{413, "upload exceeds file limit"}
	}
	return dataset.Upload(r.R.Context(), r.DB, h.Filename, body)
}
func importDataset(r *Request) (any, error) {
	done, err := multipart(r)
	if err != nil {
		return nil, err
	}
	defer done()
	name := strings.TrimSpace(r.R.FormValue("name"))
	if name == "" || len(name) > 80 {
		return nil, invalid("name must be 1-80 characters")
	}
	id, err := uploadField(r, "file", dataset.MaxFile)
	if err != nil {
		return nil, err
	}
	images := M{}
	for _, h := range r.R.MultipartForm.File["images"] {
		f, er := h.Open()
		if er != nil {
			return nil, er
		}
		b, er := io.ReadAll(io.LimitReader(f, dataset.MaxImage+1))
		f.Close()
		if er != nil || len(b) > dataset.MaxImage {
			return nil, &HTTPError{413, "image exceeds 20 MiB"}
		}
		up, er := dataset.Upload(r.R.Context(), r.DB, h.Filename, b)
		if er != nil {
			return nil, er
		}
		images[h.Filename] = up
	}
	p := M{"upload_id": id, "name": name, "description": r.R.FormValue("description"), "instructions": r.R.FormValue("instructions"), "task_type": r.R.FormValue("task_type"), "allow_partial": r.R.FormValue("allow_partial") == "true", "images": images}
	if p["task_type"] == "" {
		p["task_type"] = "generic"
	}
	return r.Async("dataset.import", p, "dataset:upload:"+id)
}
func importPredictions(r *Request) (any, error) {
	if _, err := r.DB.Get(r.R.Context(), "datasets", r.R.PathValue("id")); err != nil {
		return nil, err
	}
	done, err := multipart(r)
	if err != nil {
		return nil, err
	}
	defer done()
	if r.R.FormValue("name") == "" {
		return nil, invalid("prediction import name required")
	}
	id, err := uploadField(r, "file", dataset.MaxFile)
	if err != nil {
		return nil, err
	}
	return r.Async("predictions.import", M{"upload_id": id, "dataset_id": r.R.PathValue("id"), "name": r.R.FormValue("name")}, "predictions:"+id)
}
func deleteDataset(r *Request) (any, error) {
	id := r.R.PathValue("id")
	ds, err := r.DB.Get(r.R.Context(), "datasets", id)
	if err != nil {
		return nil, err
	}
	if domain.Bool(ds["builtin"]) {
		return nil, conflict("builtin datasets cannot be deleted")
	}
	var used int
	err = r.DB.Pool.QueryRow(r.R.Context(), "SELECT count(*) FROM arl.documents WHERE project_id=$1 AND kind='experiments' AND data->>'status'='frozen' AND data->'plan'->>'dataset_id'=$2", r.DB.Project, id).Scan(&used)
	if err != nil {
		return nil, err
	}
	if used > 0 {
		return nil, conflict("dataset is referenced by frozen experiments")
	}
	return r.Async("dataset.delete", M{"dataset_id": id}, "dataset:delete:"+id)
}
func uploadArtifact(r *Request) (any, error) {
	done, err := multipart(r)
	if err != nil {
		return nil, err
	}
	defer done()
	id, err := uploadField(r, "file", dataset.MaxImage)
	if err != nil {
		return nil, err
	}
	return r.Async("artifact.validate", M{"upload_id": id}, "artifact:"+id)
}
func getArtifact(r *Request) (any, error) {
	m, b, err := dataset.Image(r.R.Context(), r.DB, r.R.PathValue("digest"), true)
	if err != nil {
		return nil, err
	}
	r.W.Header().Set("Content-Type", domain.Str(m["mime"]))
	r.W.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	r.W.Header().Set("ETag", `"`+domain.Str(m["digest"])+`"`)
	_, err = r.W.Write(b)
	return nil, err
}
