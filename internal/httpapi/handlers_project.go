package httpapi

import (
	"net/http"

	"passpal/internal/audit"
	"passpal/internal/model"
	"passpal/internal/project"
	"passpal/internal/tag"
)

type projectBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	items, err := s.projects.List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body projectBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.projects.Create(r.Context(), project.Input{
		Name: body.Name, Description: body.Description, Icon: body.Icon,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionCreateProject, audit.TargetProject, &p.ID)
	writeData(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body projectBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.projects.Update(r.Context(), id, project.Input{
		Name: body.Name, Description: body.Description, Icon: body.Icon,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionUpdateProject, audit.TargetProject, &id)
	writeData(w, http.StatusOK, p)
}

// handleDeleteProject 返回影响范围，供前端做二次确认后的结果展示。
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	res, err := s.projects.SoftDelete(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionDeleteProject, audit.TargetProject, &id)
	writeData(w, http.StatusOK, res)
}

func (s *Server) handleRestoreProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.projects.Restore(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionRestoreItem, audit.TargetProject, &id)
	writeData(w, http.StatusOK, p)
}

type reorderBody struct {
	ProjectID int64   `json:"project_id"`
	IDs       []int64 `json:"ids"`
}

func (s *Server) handleReorderProjects(w http.ResponseWriter, r *http.Request) {
	var body reorderBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.projects.Reorder(r.Context(), body.IDs); err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// ── 标签 ────────────────────────────────────────────────────

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.projects.Get(r.Context(), projectID); err != nil {
		writeError(w, r, err)
		return
	}
	items, err := s.tags.ListByProject(r.Context(), projectID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": items})
}

type tagBody struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body tagBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := s.tags.Create(r.Context(), projectID, tag.Input{Name: body.Name, Color: body.Color})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionCreateTag, audit.TargetTag, &t.ID)
	writeData(w, http.StatusCreated, t)
}

func (s *Server) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body tagBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	t, err := s.tags.Update(r.Context(), id, tag.Input{Name: body.Name, Color: body.Color})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionUpdateTag, audit.TargetTag, &id)
	writeData(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.tags.SoftDelete(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionDeleteTag, audit.TargetTag, &id)
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRestoreTag(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	t, err := s.tags.Restore(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionRestoreItem, audit.TargetTag, &id)
	writeData(w, http.StatusOK, t)
}

func (s *Server) handleReorderTags(w http.ResponseWriter, r *http.Request) {
	var body reorderBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.ProjectID <= 0 {
		writeError(w, r, model.ErrValidation("project_required", "缺少项目 ID"))
		return
	}
	if err := s.tags.Reorder(r.Context(), body.ProjectID, body.IDs); err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}
