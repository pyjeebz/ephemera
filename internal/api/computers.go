package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pyjeebz/ephemera/internal/machine"
	"github.com/pyjeebz/ephemera/internal/store"
)

// A computer is a named, persistent machine. Creating one makes its disk and
// boots it; stopping throws the running machine away but keeps the disk; starting
// boots a fresh machine on that same disk, so its state is back. This is the
// "use it like a laptop" surface on top of the persist-disk mechanism.

// ComputerResponse describes a computer to a client, including its running
// machine when it has one.
type ComputerResponse struct {
	Name      string    `json:"name"`
	Running   bool      `json:"running"`
	MachineID string    `json:"machine_id,omitempty"`
	GuestIP   string    `json:"guest_ip,omitempty"`
	VsockPath string    `json:"vsock_path,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) computerResponse(c store.ComputerRecord) ComputerResponse {
	resp := ComputerResponse{Name: c.Name, CreatedAt: c.CreatedAt}
	if m, ok := s.runningMachine(c.Name); ok {
		resp.Running = true
		resp.MachineID = m.ID
		resp.GuestIP = m.GuestIP
		resp.VsockPath = m.VsockPath
	}
	return resp
}

// runningMachine finds the machine currently backing a computer, if any.
func (s *Server) runningMachine(computer string) (store.Record, bool) {
	for _, r := range s.store.List() {
		if r.Computer == computer {
			return r, true
		}
	}
	return store.Record{}, false
}

// bootComputer boots a machine on a computer's disk and links the two.
func (s *Server) bootComputer(c store.ComputerRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.BootTimeout)
	defer cancel()

	cfg := s.machineConfig()
	cfg.PersistDisk = c.DiskPath
	// A computer you keep wants the internet — to install things — so give it a
	// network whenever the daemon can.
	cfg.Net = s.cfg.Net

	_, err := s.bootAndTrack(ctx, cfg, c.Name)
	return err
}

func (s *Server) createComputer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}

	// Reserve the name first so a bad or taken name fails before any disk is made.
	rec, err := s.computers.Create(req.Name, s.computers.DiskPath(req.Name))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := machine.MakePersistDisk(rec.DiskPath, 0); err != nil {
		_ = s.computers.Remove(rec.Name)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.bootComputer(rec); err != nil {
		_ = s.computers.Remove(rec.Name)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	s.log.Info("computer created", "name", rec.Name)
	writeJSON(w, http.StatusCreated, s.computerResponse(rec))
}

func (s *Server) listComputers(w http.ResponseWriter, _ *http.Request) {
	recs := s.computers.List()
	out := make([]ComputerResponse, 0, len(recs))
	for _, c := range recs {
		out = append(out, s.computerResponse(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"computers": out})
}

func (s *Server) getComputer(w http.ResponseWriter, r *http.Request) {
	c, err := s.computers.Get(r.PathValue("name"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, s.computerResponse(c))
}

func (s *Server) startComputer(w http.ResponseWriter, r *http.Request) {
	c, err := s.computers.Get(r.PathValue("name"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if _, ok := s.runningMachine(c.Name); ok {
		// Already running — starting is idempotent, so just report it.
		writeJSON(w, http.StatusOK, s.computerResponse(c))
		return
	}
	if err := s.bootComputer(c); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("computer started", "name", c.Name)
	writeJSON(w, http.StatusOK, s.computerResponse(c))
}

func (s *Server) stopComputer(w http.ResponseWriter, r *http.Request) {
	c, err := s.computers.Get(r.PathValue("name"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if err := s.stopRunning(c.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("computer stopped", "name", c.Name)
	writeJSON(w, http.StatusOK, s.computerResponse(c))
}

func (s *Server) deleteComputer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.computers.Get(name); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	// Stop the running machine before deleting the disk out from under it.
	if err := s.stopRunning(name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.computers.Remove(name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Info("computer deleted", "name", name)
	w.WriteHeader(http.StatusNoContent)
}

// stopRunning destroys the machine backing a computer, if one is running, and
// leaves the disk alone. A computer with nothing running is a no-op.
//
// It flushes the guest's filesystem cache to the persist disk first. The machine
// has no graceful shutdown — stopping it means killing the VMM — so anything
// still in the guest's page cache would be lost. A sync pushes it to the disk,
// which is what makes a clean stop actually keep recent work.
func (s *Server) stopRunning(computer string) error {
	rec, ok := s.runningMachine(computer)
	if !ok {
		return nil
	}
	m, _, err := s.store.Get(rec.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}

	syncCtx, syncCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, _ = m.Exec(syncCtx, []string{"sync"}, io.Discard, io.Discard)
	syncCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Destroy(ctx); err != nil {
		return err
	}
	return s.store.Remove(rec.ID)
}
