package server

import (
	"errors"
	"strconv"

	"go.vicoworks.com/agent-reliability-lab/internal/domain"
	"go.vicoworks.com/agent-reliability-lab/internal/prompts"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
)

func (s *Server) promptRoutes() {
	s.route("GET /api/prompts", func(r *Request) (any, error) {
		rows, err := r.DB.List(r.R.Context(), prompts.Prompts, nil, 1000)
		if err != nil {
			return nil, err
		}
		store.Order(rows, "updated_at", true)
		return rows, nil
	})
	// POST /api/prompts {name, description, template, config, message, labels}
	// commits a version, creating the prompt when needed.
	s.route("POST /api/prompts", func(r *Request) (any, error) {
		body, err := r.Body(1 << 20)
		if err != nil {
			return nil, err
		}
		return commitPrompt(r, domain.Str(body["name"]), body)
	})
	s.route("POST /api/prompts/{name}/versions", func(r *Request) (any, error) {
		body, err := r.Body(1 << 20)
		if err != nil {
			return nil, err
		}
		if _, err = r.DB.Get(r.R.Context(), prompts.Prompts, r.R.PathValue("name")); err != nil {
			return nil, err
		}
		return commitPrompt(r, r.R.PathValue("name"), body)
	})
	// GET /api/prompts/{name}?label=staging | ?version=3 resolves one version.
	s.route("GET /api/prompts/{name}", func(r *Request) (any, error) {
		version, _ := strconv.Atoi(r.R.URL.Query().Get("version"))
		prompt, v, err := prompts.Resolve(r.R.Context(), r.DB, r.R.PathValue("name"), r.R.URL.Query().Get("label"), version)
		if err != nil {
			return nil, err
		}
		v = domain.Copy(v)
		v["labels"], v["description"], v["latest"] = prompt["labels"], prompt["description"], prompt["latest"]
		return v, nil
	})
	s.route("GET /api/prompts/{name}/versions", func(r *Request) (any, error) {
		rows, err := r.DB.List(r.R.Context(), prompts.Versions, M{"prompt": r.R.PathValue("name")}, 5000)
		if err != nil {
			return nil, err
		}
		store.Order(rows, "version", true)
		return rows, nil
	})
	s.route("POST /api/prompts/{name}/labels", func(r *Request) (any, error) {
		body, err := r.Body(4096)
		if err != nil {
			return nil, err
		}
		prompt, err := prompts.SetLabel(r.R.Context(), r.DB, r.R.PathValue("name"), domain.Str(body["label"]), domain.Int(body["version"]))
		if errors.Is(err, prompts.ErrInvalid) {
			return nil, invalid(err.Error())
		}
		if err == nil {
			_ = r.DB.Audit(r.R.Context(), "prompt.label", r.R.PathValue("name"), M{"label": body["label"], "version": body["version"]})
		}
		return prompt, err
	})
	s.route("POST /api/prompts/{name}/render", func(r *Request) (any, error) {
		body, err := r.Body(1 << 20)
		if err != nil {
			return nil, err
		}
		_, v, err := prompts.Resolve(r.R.Context(), r.DB, r.R.PathValue("name"), domain.Str(body["label"]), domain.Int(body["version"]))
		if err != nil {
			return nil, err
		}
		rendered, missing := prompts.Render(v, domain.Obj(body["variables"]))
		rendered["missing"] = missing
		return rendered, nil
	})
	s.route("DELETE /api/prompts/{name}", func(r *Request) (any, error) {
		if err := prompts.Delete(r.R.Context(), r.DB, r.R.PathValue("name")); err != nil {
			return nil, err
		}
		_ = r.DB.Audit(r.R.Context(), "prompt.delete", r.R.PathValue("name"), M{})
		return M{"deleted": r.R.PathValue("name")}, nil
	})
}

func commitPrompt(r *Request, name string, body M) (any, error) {
	version, created, err := prompts.Commit(r.R.Context(), r.DB, name, body)
	if errors.Is(err, prompts.ErrInvalid) {
		return nil, invalid(err.Error())
	}
	if err != nil {
		return nil, err
	}
	if created {
		_ = r.DB.Audit(r.R.Context(), "prompt.commit", name, M{"version": version["version"]})
	}
	version = domain.Copy(version)
	version["created"] = created
	return version, nil
}
