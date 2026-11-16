package dataset

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/scoring"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sort"
	"strings"
)

type M = domain.M

//go:embed fixtures.json.gz
var fixtures []byte

const MaxImage = 20 << 20
const MaxFile = 100 << 20

func PutImage(ctx context.Context, s *store.Store, data []byte) (M, error) {
	if len(data) == 0 || len(data) > MaxImage {
		return nil, errors.New("image exceeds 20 MiB or is empty")
	}
	h := sha256.Sum256(data)
	digest := hex.EncodeToString(h[:])
	if m, _, e := Image(ctx, s, digest, false); e == nil {
		return m, nil
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil {
		return nil, errors.New("invalid image")
	}
	mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif"}[format]
	if mime == "" || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
		return nil, errors.New("unsupported image or more than 40 million pixels")
	}
	if _, _, e = image.Decode(bytes.NewReader(data)); e != nil {
		return nil, errors.New("image failed full decode validation")
	}
	_, e = s.Pool.Exec(ctx, "INSERT INTO arl.artifacts(project_id,digest,mime,bytes,width,height,content) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING", s.Project, digest, mime, len(data), cfg.Width, cfg.Height, data)
	return M{"digest": digest, "mime": mime, "bytes": len(data), "width": cfg.Width, "height": cfg.Height}, e
}
func Image(ctx context.Context, s *store.Store, digest string, content bool) (M, []byte, error) {
	var mime string
	var n, w, h int
	var b []byte
	query := "SELECT mime,bytes,width,height FROM arl.artifacts WHERE project_id=$1 AND digest=$2"
	var e error
	if content {
		query = "SELECT mime,bytes,width,height,content FROM arl.artifacts WHERE project_id=$1 AND digest=$2"
		e = s.Pool.QueryRow(ctx, query, s.Project, digest).Scan(&mime, &n, &w, &h, &b)
	} else {
		e = s.Pool.QueryRow(ctx, query, s.Project, digest).Scan(&mime, &n, &w, &h)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil, store.ErrNotFound
	}
	return M{"digest": digest, "mime": mime, "bytes": n, "width": w, "height": h}, b, e
}
func Upload(ctx context.Context, s *store.Store, name string, body []byte) (string, error) {
	if len(body) > MaxFile {
		return "", errors.New("file too large")
	}
	id := domain.ID("upload")
	_, e := s.Pool.Exec(ctx, "INSERT INTO arl.uploads(id,project_id,filename,content) VALUES($1,$2,$3,$4)", id, s.Project, name, body)
	return id, e
}
func ReadUpload(ctx context.Context, s *store.Store, id string) (string, []byte, error) {
	var name string
	var body []byte
	e := s.Pool.QueryRow(ctx, "SELECT filename,content FROM arl.uploads WHERE id=$1 AND project_id=$2", id, s.Project).Scan(&name, &body)
	return name, body, e
}
func Parse(content []byte, name string) ([]M, error) {
	out := []M{}
	trim := bytes.TrimSpace(content)
	if strings.HasSuffix(strings.ToLower(name), ".csv") || strings.HasSuffix(strings.ToLower(name), ".tsv") {
		r := csv.NewReader(bytes.NewReader(content))
		if strings.HasSuffix(strings.ToLower(name), ".tsv") {
			r.Comma = '\t'
		}
		head, e := r.Read()
		if e != nil {
			return nil, e
		}
		for {
			line, e := r.Read()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			m := M{}
			for i, k := range head {
				if i < len(line) {
					var v any
					if json.Unmarshal([]byte(line[i]), &v) != nil {
						v = line[i]
					}
					m[strings.TrimSpace(k)] = v
				}
			}
			out = append(out, m)
			if len(out) > 50000 {
				return nil, errors.New("too many samples")
			}
		}
		return out, nil
	}
	if len(trim) == 0 {
		return nil, errors.New("empty file")
	}
	var v any
	if json.Unmarshal(trim, &v) == nil {
		switch value := v.(type) {
		case []any:
			return domain.Maps(value), nil
		case map[string]any:
			for _, k := range []string{"samples", "records", "data"} {
				if a, ok := value[k].([]any); ok {
					return domain.Maps(a), nil
				}
			}
			return []M{value}, nil
		}
	}
	scan := bufio.NewScanner(bytes.NewReader(content))
	scan.Buffer(make([]byte, 4096), 8<<20)
	for scan.Scan() {
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue
		}
		var m M
		if e := json.Unmarshal(line, &m); e != nil {
			return nil, fmt.Errorf("invalid JSONL at record %d", len(out)+1)
		}
		out = append(out, m)
		if len(out) > 50000 {
			return nil, errors.New("too many samples")
		}
	}
	return out, scan.Err()
}
func first(m M, keys ...string) any {
	for _, k := range keys {
		if v := m[k]; v != nil && v != "" {
			return v
		}
	}
	return nil
}
func Import(ctx context.Context, s *store.Store, p M) (M, error) {
	name, body, e := ReadUpload(ctx, s, domain.Str(p["upload_id"]))
	if e != nil {
		return nil, e
	}
	records, e := Parse(body, name)
	if e != nil {
		return nil, e
	}
	if len(records) > 50000 {
		return nil, errors.New("too many samples")
	}
	samples, invalid := []M{}, []M{}
	seen := map[string]bool{}
	imageMap := domain.Obj(p["images"])
	for i, row := range records {
		reject := func(reason string) { invalid = append(invalid, M{"record": i + 1, "error": reason}) }
		raw := first(row, "input", "prompt", "question", "text", "query")
		text := ""
		refs := []string{}
		if m, ok := raw.(map[string]any); ok {
			text = domain.Str(first(m, "text", "prompt"))
			refs = append(refs, domain.Strings(m["images"])...)
		} else {
			text = domain.Str(raw)
		}
		for _, k := range []string{"image", "images"} {
			switch v := row[k].(type) {
			case string:
				for _, ref := range strings.Split(v, "|") {
					if strings.TrimSpace(ref) != "" {
						refs = append(refs, strings.TrimSpace(ref))
					}
				}
			case []any:
				refs = append(refs, domain.Strings(v)...)
			}
		}
		if text == "" && len(refs) == 0 {
			reject("missing input (text or image)")
			continue
		}
		if len([]rune(text)) > 200000 {
			reject("input text exceeds 200000 characters")
			continue
		}
		digests := []string{}
		bad := false
		for _, ref := range refs {
			var b []byte
			if id := domain.Str(imageMap[ref]); id != "" {
				_, b, e = ReadUpload(ctx, s, id)
			} else if strings.HasPrefix(ref, "data:image") {
				_, v, ok := strings.Cut(ref, ",")
				if !ok {
					e = errors.New("invalid image data URL")
				} else {
					b, e = base64.StdEncoding.DecodeString(v)
				}
			} else {
				_, _, e = Image(ctx, s, ref, false)
				if e == nil {
					digests = append(digests, ref)
					continue
				}
			}
			if e != nil {
				reject("image not provided or invalid")
				bad = true
				break
			}
			m, e := PutImage(ctx, s, b)
			if e != nil {
				reject(e.Error())
				bad = true
				break
			}
			digests = append(digests, domain.Str(m["digest"]))
		}
		if bad {
			continue
		}
		key := domain.Str(first(row, "id", "key"))
		if key == "" {
			key = fmt.Sprintf("s%05d", i+1)
		}
		if seen[key] {
			reject("duplicate sample id: " + key)
			continue
		}
		seen[key] = true
		split := strings.ToLower(domain.Str(row["split"]))
		if split == "" {
			split = "test"
		}
		if !domain.Has([]string{"train", "dev", "calibration", "test", "holdout"}, split) {
			reject("unknown split")
			continue
		}
		meta := domain.Obj(row["meta"])
		for k, v := range row {
			if !domain.Has([]string{"id", "key", "input", "prompt", "question", "text", "query", "expected", "answer", "label", "target", "reference", "output", "split", "group", "group_id", "meta", "image", "images"}, k) {
				if _, ok := meta[k]; !ok {
					meta[k] = v
				}
			}
		}
		exp := first(row, "expected", "answer", "label", "target", "reference", "output")
		if str, ok := exp.(string); ok {
			var parsed any
			if json.Unmarshal([]byte(str), &parsed) == nil {
				exp = parsed
			}
		}
		input := M{"text": text, "images": digests}
		contentHash := domain.Hash(M{"text": text, "images": digests, "expected": exp})
		samples = append(samples, M{"id": "", "key": key, "ordinal": len(samples), "split": split, "group": first(row, "group", "group_id"), "input": input, "expected": exp, "meta": meta, "content_hash": contentHash})
	}
	if len(invalid) > 0 && !domain.Bool(p["allow_partial"]) {
		return M{"validation_error": true, "message": fmt.Sprintf("%d invalid record(s)", len(invalid)), "errors": invalid[:domain.Min(50, len(invalid))]}, nil
	}
	if len(samples) == 0 {
		return M{"validation_error": true, "message": "dataset has no valid samples", "errors": invalid}, nil
	}
	ds, e := Create(ctx, s, domain.Str(p["name"]), samples, M{"description": domain.Str(p["description"]), "instructions": domain.Str(p["instructions"]), "task_type": domain.Str(p["task_type"]), "card": M{"origin": "user import", "source_file": name, "rejected_records": len(invalid)}})
	if e != nil {
		return nil, e
	}
	ds["rejected"] = invalid[:domain.Min(50, len(invalid))]
	return ds, nil
}
func Create(ctx context.Context, s *store.Store, name string, samples []M, extra M) (M, error) {
	if strings.TrimSpace(name) == "" || len(name) > 80 {
		return nil, errors.New("name must be 1-80 characters")
	}
	ordered := append([]M{}, samples...)
	sort.Slice(ordered, func(i, j int) bool { return domain.Str(ordered[i]["key"]) < domain.Str(ordered[j]["key"]) })
	digest := domain.Hash(ordered)
	id := "ds_" + domain.Hash([]any{name, digest})[:24]
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", s.Project+":dataset:"+name)
	if e != nil {
		return nil, e
	}
	var existing M
	e = tx.QueryRow(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind='datasets' AND id=$2", s.Project, id).Scan(&existing)
	if e == nil {
		return existing, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	var version int
	e = tx.QueryRow(ctx, "SELECT COALESCE(max((data->>'version')::integer),0)+1 FROM arl.documents WHERE project_id=$1 AND kind='datasets' AND data->>'name'=$2", s.Project, name).Scan(&version)
	if e != nil {
		return nil, e
	}
	rows := make([][]any, 0, len(samples))
	modality := "text"
	splits := M{}
	exact, near, groups := map[string]map[string]bool{}, map[string]map[string]bool{}, map[string]map[string]bool{}
	add := func(m map[string]map[string]bool, k, split string) {
		if m[k] == nil {
			m[k] = map[string]bool{}
		}
		m[k][split] = true
	}
	for i, sample := range samples {
		sample = domain.Copy(sample)
		sample["dataset_id"] = id
		sample["id"] = "smp_" + domain.Hash([]any{id, sample["key"]})[:24]
		sample["ordinal"] = i
		rows = append(rows, []any{s.Project, "samples", sample["id"], domain.JSONB(sample)})
		if len(domain.Arr(domain.Obj(sample["input"])["images"])) > 0 {
			modality = "image"
		}
		split := domain.Str(sample["split"])
		splits[split] = domain.Int(splits[split]) + 1
		add(exact, domain.Str(sample["content_hash"]), split)
		add(near, domain.Hash([]any{scoring.Normalize(domain.Obj(sample["input"])["text"]), domain.Obj(sample["input"])["images"]}), split)
		if group := domain.Str(sample["group"]); group != "" {
			add(groups, group, split)
		}
	}
	if _, e = tx.CopyFrom(ctx, pgx.Identifier{"arl", "documents"}, []string{"project_id", "kind", "id", "data"}, pgx.CopyFromRows(rows)); e != nil {
		return nil, e
	}
	count := func(m map[string]map[string]bool) int {
		n := 0
		for _, s := range m {
			if len(s) > 1 {
				n++
			}
		}
		return n
	}
	cross := []string{}
	for g, v := range groups {
		if len(v) > 1 {
			cross = append(cross, g)
		}
	}
	sort.Strings(cross)
	card := domain.Obj(extra["card"])
	card["splits"] = splits
	card["leakage"] = M{"exact_cross_split_duplicates": count(exact), "near_cross_split_duplicates": count(near), "groups_spanning_splits": cross, "method": "content hash + normalized text and image digests; group IDs"}
	ds := domain.Merge(M{"id": id, "name": name, "version": version, "digest": digest, "modality": modality, "task_type": "generic", "description": "", "instructions": "", "sample_count": len(samples), "builtin": false, "synthetic": false, "created_at": domain.Now()}, extra)
	ds["card"] = card
	_, e = tx.Exec(ctx, "INSERT INTO arl.documents(project_id,kind,id,data) VALUES($1,'datasets',$2,$3)", s.Project, id, domain.JSONB(ds))
	if e != nil {
		return nil, e
	}
	return ds, tx.Commit(ctx)
}
func Seed(ctx context.Context, s *store.Store) (M, error) {
	reader, e := gzip.NewReader(bytes.NewReader(fixtures))
	if e != nil {
		return nil, e
	}
	defer reader.Close()
	var data struct{ Datasets, Samples, Artifacts []M }
	if e = json.NewDecoder(reader).Decode(&data); e != nil {
		return nil, e
	}
	for _, a := range data.Artifacts {
		b, e := base64.StdEncoding.DecodeString(domain.Str(a["content"]))
		if e != nil {
			return nil, e
		}
		_, e = s.Pool.Exec(ctx, "INSERT INTO arl.artifacts(project_id,digest,mime,bytes,width,height,content) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING", s.Project, a["digest"], a["mime"], domain.Int(a["bytes"]), domain.Int(a["width"]), domain.Int(a["height"]), b)
		if e != nil {
			return nil, e
		}
	}
	safety, probes := Safety()
	data.Samples = append(data.Samples, probes...)
	data.Datasets = append(data.Datasets, safety)
	if e = s.Batch(ctx, "samples", data.Samples); e != nil {
		return nil, e
	}
	if e = s.Batch(ctx, "datasets", data.Datasets); e != nil {
		return nil, e
	}
	return M{"datasets": len(data.Datasets), "samples": len(data.Samples), "images": len(data.Artifacts)}, nil
}
func Predictions(ctx context.Context, s *store.Store, p M) (M, error) {
	name, b, e := ReadUpload(ctx, s, domain.Str(p["upload_id"]))
	if e != nil {
		return nil, e
	}
	records, e := Parse(b, name)
	if e != nil {
		return nil, e
	}
	samples, e := s.List(ctx, "samples", M{"dataset_id": p["dataset_id"]}, 50000)
	if e != nil {
		return nil, e
	}
	keys := map[string]bool{}
	for _, sample := range samples {
		keys[domain.Str(sample["key"])] = true
	}
	docs, unknown := []M{}, []string{}
	for _, row := range records {
		key := domain.Str(first(row, "id", "key"))
		out := first(row, "output", "prediction")
		if !keys[key] || out == nil {
			unknown = append(unknown, key)
			continue
		}
		text, ok := out.(string)
		if !ok {
			text = string(domain.JSONB(out))
		}
		docs = append(docs, M{"id": "pred_" + domain.Hash([]any{p["dataset_id"], p["name"], key})[:24], "import_name": p["name"], "dataset_id": p["dataset_id"], "sample_key": key, "output_text": text, "created_at": domain.Now()})
	}
	if len(docs) == 0 {
		return M{"validation_error": true, "message": "no rows matched dataset sample IDs", "errors": []any{}}, nil
	}
	for _, doc := range docs {
		if e = s.Put(ctx, "predictions", domain.Str(doc["id"]), doc); e != nil {
			return nil, e
		}
	}
	return M{"imported": len(docs), "unmatched": unknown[:domain.Min(50, len(unknown))]}, nil
}
