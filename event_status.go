package main

import (
	"net/http"
)

func (s *Server) eventAuthorized(w http.ResponseWriter, r *http.Request) bool {
	return s.authorizeEvent(w, r)
}

// Automation clients get task progress, never administrator settings, provider
// credentials, playback links or the full job snapshot.
func (s *Server) eventStatus(w http.ResponseWriter, r *http.Request) {
	if !s.eventAuthorized(w, r) {
		return
	}
	// Filter by the client's authorized rules before truncating: taking the
	// global newest 20 first would let one busy rule crowd out every other
	// rule's jobs. Legacy access is authorized for all rules, so the plain
	// newest-20 window is already correctly scoped for it.
	var jobs []Job
	var err error
	if access, ok := r.Context().Value(eventAccessKey{}).(eventAccess); ok && !access.Legacy {
		jobs, err = s.s.recentJobsForRules(access.Rules, 20)
	} else {
		jobs, err = s.s.jobs(20)
	}
	if err != nil {
		fail(w, 500, "任务状态读取失败")
		return
	}
	events, err := s.s.ingestEventsFiltered(func(rule string) bool { return eventRuleAllowed(r, rule) })
	if err != nil {
		fail(w, 500, "入库状态读取失败")
		return
	}
	items := make([]object, 0, len(jobs))
	for _, j := range jobs {
		if !eventRuleAllowed(r, j.Rule) {
			continue
		}
		items = append(items, object{"id": j.ID, "rule": j.Rule, "source_path": j.SourcePath, "status": j.Status, "created": j.Created, "finished": j.Finished, "created_files": j.CreatedFiles, "skipped": j.Skipped, "errors": j.Errors, "message": cleanRecordText(j.Message, 300), "current_path": j.CurrentPath})
	}
	filtered := []IngestEvent{}
	for _, v := range events {
		if eventRuleAllowed(r, v.Rule) {
			filtered = append(filtered, v)
		}
	}
	jsonOut(w, 200, object{"jobs": items, "events": filtered})
}
