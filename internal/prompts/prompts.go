// Package prompts is a versioned prompt registry. A prompt has immutable
// numbered versions (template + model configuration) and movable labels such
// as "production" or "staging", so agents fetch "support@production" while
// the team iterates on new versions in the Workbench.
package prompts

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/VicoAritonang/lab-of-night-and-flame/internal/domain"
	"github.com/VicoAritonang/lab-of-night-and-flame/internal/store"
)

type M = domain.M

const (
	Prompts  = "prompts"
	Versions = "prompt_versions"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
var labelPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var variablePattern = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}\}`)

var ErrInvalid = errors.New("invalid prompt")

func invalid(msg string) error { return errors.Join(ErrInvalid, errors.New(msg)) }

// ValidName reports whether name is a prompt slug.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Template normalizes a version body: a system prompt and/or chat messages
// (a plain "text" template is stored as a single user message).
func Template(raw M) (M, error) {
	out := M{"system": strings.TrimRight(domain.Str(raw["system"]), " \t"), "messages": []M{}}
	if text := domain.Str(raw["text"]); text != "" {
		out["messages"] = []M{{"role": "user", "content": text}}
	}
	if msgs, ok := raw["messages"]; ok && msgs != nil {
		list := []M{}
		for _, m := range domain.Maps(msgs) {
			role := domain.Str(m["role"])
			if !domain.Has([]string{"system", "user", "assistant"}, role) {
				return nil, invalid("message roles must be system, user or assistant")
			}
			content, ok := m["content"].(string)
			if !ok {
				return nil, invalid("message content must be a string")
			}
			list = append(list, M{"role": role, "content": content})
		}
		if len(list) > 0 {
			out["messages"] = list
		}
	}
	if out["system"] == "" && len(domain.Maps(out["messages"])) == 0 {
		return nil, invalid("template needs system, messages or text")
	}
	if len(domain.JSON(out)) > 256<<10 {
		return nil, invalid("template exceeds 256 KiB")
	}
	return out, nil
}

// Variables lists the {{variables}} a template uses, sorted.
func Variables(template M) []string {
	seen := map[string]bool{}
	texts := []string{domain.Str(template["system"])}
	for _, m := range domain.Maps(template["messages"]) {
		texts = append(texts, domain.Str(m["content"]))
	}
	for _, t := range texts {
		for _, m := range variablePattern.FindAllStringSubmatch(t, -1) {
			seen[m[1]] = true
		}
	}
	out := []string{}
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func lookup(vars M, path string) (any, bool) {
	var cur any = vars
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func substitute(text string, vars M, missing map[string]bool) string {
	return variablePattern.ReplaceAllStringFunc(text, func(token string) string {
		name := variablePattern.FindStringSubmatch(token)[1]
		v, ok := lookup(vars, name)
		if !ok {
			missing[name] = true
			return token
		}
		if s, isString := v.(string); isString {
			return s
		}
		return string(domain.JSON(v))
	})
}

// Render fills a version's template; missing variables are reported and left
// in place.
func Render(version M, vars M) (M, []string) {
	template := domain.Obj(version["template"])
	missing := map[string]bool{}
	messages := []M{}
	system := substitute(domain.Str(template["system"]), vars, missing)
	if system != "" {
		messages = append(messages, M{"role": "system", "content": system})
	}
	for _, m := range domain.Maps(template["messages"]) {
		messages = append(messages, M{"role": m["role"], "content": substitute(domain.Str(m["content"]), vars, missing)})
	}
	names := []string{}
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	return M{"system": system, "messages": messages, "config": version["config"], "name": version["prompt"], "version": version["version"]}, names
}

func versionID(name string, v int) string { return name + "@" + strconv.Itoa(v) }

// Commit adds a version (creating the prompt on first commit). A body equal to
// the latest version returns that version instead of a duplicate.
func Commit(ctx context.Context, db *store.Store, name string, body M) (M, bool, error) {
	if !ValidName(name) {
		return nil, false, invalid("name must be a lowercase slug (a-z, 0-9, '.', '_', '-')")
	}
	template, err := Template(domain.Obj(body["template"]))
	if err != nil {
		return nil, false, err
	}
	config := M{}
	for k, v := range domain.Obj(body["config"]) {
		if domain.Has([]string{"model", "provider", "params"}, k) {
			config[k] = v
		}
	}
	labels := domain.Strings(body["labels"])
	for _, l := range labels {
		if !labelPattern.MatchString(l) || l == "latest" {
			return nil, false, invalid("labels are lowercase words; 'latest' is reserved")
		}
	}
	digest := domain.Hash(M{"template": template, "config": config})
	now := domain.Now()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	// Serialize commits per prompt.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", db.Project+"/prompt/"+name); err != nil {
		return nil, false, err
	}
	var prompt M
	err = tx.QueryRow(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind=$2 AND id=$3", db.Project, Prompts, name).Scan(&prompt)
	created := err != nil
	if created {
		prompt = M{"id": name, "name": name, "description": domain.Str(body["description"]), "latest": 0, "labels": M{}, "created_at": now}
	}
	latest := domain.Int(prompt["latest"])
	if latest > 0 {
		var previous M
		if err = tx.QueryRow(ctx, "SELECT data FROM arl.documents WHERE project_id=$1 AND kind=$2 AND id=$3", db.Project, Versions, versionID(name, latest)).Scan(&previous); err == nil && previous["digest"] == digest && len(labels) == 0 {
			return previous, false, nil
		}
	}
	v := latest + 1
	version := M{"id": versionID(name, v), "prompt": name, "version": v, "template": template, "config": config, "variables": Variables(template), "message": domain.Str(body["message"]), "author": domain.Str(body["author"]), "digest": digest, "created_at": now}
	if _, err = tx.Exec(ctx, "INSERT INTO arl.documents(project_id,kind,id,data) VALUES($1,$2,$3,$4)", db.Project, Versions, version["id"], domain.JSONB(version)); err != nil {
		return nil, false, err
	}
	prompt["latest"], prompt["updated_at"] = v, now
	if d := domain.Str(body["description"]); d != "" {
		prompt["description"] = d
	}
	l := domain.Obj(prompt["labels"])
	for _, label := range labels {
		l[label] = v
	}
	prompt["labels"] = l
	if _, err = tx.Exec(ctx, `INSERT INTO arl.documents(project_id,kind,id,data) VALUES($1,$2,$3,$4)
 ON CONFLICT(project_id,kind,id) DO UPDATE SET data=EXCLUDED.data,version=arl.documents.version+1,updated_at=now()`, db.Project, Prompts, name, domain.JSONB(prompt)); err != nil {
		return nil, false, err
	}
	return version, true, tx.Commit(ctx)
}

// Resolve finds a version by number, by label, or (neither given) the
// "production" label when set, else the latest version.
func Resolve(ctx context.Context, db *store.Store, name, label string, version int) (M, M, error) {
	prompt, err := db.Get(ctx, Prompts, name)
	if err != nil {
		return nil, nil, err
	}
	labels := domain.Obj(prompt["labels"])
	switch {
	case version > 0:
	case label != "" && label != "latest":
		if labels[label] == nil {
			return nil, nil, store.ErrNotFound
		}
		version = domain.Int(labels[label])
	case label == "" && labels["production"] != nil:
		version = domain.Int(labels["production"])
	default:
		version = domain.Int(prompt["latest"])
	}
	v, err := db.Get(ctx, Versions, versionID(name, version))
	return prompt, v, err
}

// SetLabel points label at version (or removes it when version is 0).
func SetLabel(ctx context.Context, db *store.Store, name, label string, version int) (M, error) {
	if !labelPattern.MatchString(label) || label == "latest" {
		return nil, invalid("labels are lowercase words; 'latest' is reserved")
	}
	if version > 0 {
		if _, err := db.Get(ctx, Versions, versionID(name, version)); err != nil {
			return nil, err
		}
	}
	return db.Update(ctx, Prompts, name, func(p M) error {
		labels := domain.Obj(p["labels"])
		if version > 0 {
			labels[label] = version
		} else {
			delete(labels, label)
		}
		p["labels"], p["updated_at"] = labels, domain.Now()
		return nil
	})
}

// Delete removes a prompt with all its versions.
func Delete(ctx context.Context, db *store.Store, name string) error {
	if _, err := db.Get(ctx, Prompts, name); err != nil {
		return err
	}
	_, err := db.Pool.Exec(ctx, "DELETE FROM arl.documents WHERE project_id=$1 AND ((kind=$2 AND id=$3) OR (kind=$4 AND data->>'prompt'=$3))", db.Project, Prompts, name, Versions)
	return err
}
