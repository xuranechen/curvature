package api

func (h *StreamHub) beginSessionEdit(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.edits[key] {
		return false
	}
	if h.edits == nil {
		h.edits = map[string]bool{}
	}
	h.edits[key] = true
	state := h.ensurePendingSessionLocked(key)
	state.QueueFrozen = true
	return true
}

func (h *StreamHub) sessionEditIdle(key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	state := h.pendingSessions[key]
	return h.jobs[key] == 0 && (state == nil || !state.Active)
}

func (h *StreamHub) finishSessionEdit(key string, start bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.edits, key)
	state := h.ensurePendingSessionLocked(key)
	if start {
		state.Active = true
	}
	state.QueueFrozen = true
	delete(h.completed, key)
	h.clearReplayStatesForSessionLocked(key)
}

func (h *StreamHub) trackSessionJob(key string, delta int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.jobs == nil {
		h.jobs = map[string]int{}
	}
	h.jobs[key] += delta
	if h.jobs[key] == 0 {
		delete(h.jobs, key)
	}
}
