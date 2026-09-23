package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// WALEntry represents a single logged event in the Write-Ahead Log.
type WALEntry struct {
	Action  string `json:"action"` // "PLACE" or "CANCEL"
	Order   *Order `json:"order,omitempty"`
	OrderID uint64 `json:"order_id,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
}

func (e *WALEntry) validate() error {
	switch e.Action {
	case "PLACE":
		if e.Order == nil {
			return errors.New("PLACE entry has no order payload")
		}
		if e.Order.ID == 0 || e.Order.Symbol == "" {
			return errors.New("PLACE entry missing order ID or symbol")
		}
		if e.Order.Side != Buy && e.Order.Side != Sell {
			return fmt.Errorf("invalid side %d in PLACE entry", e.Order.Side)
		}
		if e.Order.Type != Limit && e.Order.Type != Market {
			return fmt.Errorf("invalid type %d in PLACE entry", e.Order.Type)
		}
		if e.Order.Amount == 0 || e.Order.Amount > MaxOrderAmount {
			return fmt.Errorf("invalid amount %d in PLACE entry", e.Order.Amount)
		}
		if e.Order.Type == Limit && (e.Order.Price == 0 || e.Order.Price > MaxOrderPrice) {
			return fmt.Errorf("invalid limit price %d in PLACE entry", e.Order.Price)
		}
	case "CANCEL":
		if e.Symbol == "" || e.OrderID == 0 {
			return errors.New("CANCEL entry missing symbol or order_id")
		}
	default:
		return fmt.Errorf("unknown action %q", e.Action)
	}
	return nil
}

// WAL manages the append-only log file on disk.
type WAL struct {
	mu     sync.Mutex
	file   *os.File
	size   int64 // length of the known-good prefix of the file
	broken error // once set, the log can no longer be trusted and every append fails
}

// OpenWAL opens or creates the WAL log file, ensuring parent directories exist.
func OpenWAL(path string) (*WAL, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	// Seek to end so subsequent writes append rather than overwrite.
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		file.Close()
		return nil, err
	}
	return &WAL{file: file, size: info.Size()}, nil
}

// LogPlace durably records an order placement (write + fsync).
func (w *WAL) LogPlace(order *Order) error {
	return w.append(WALEntry{Action: "PLACE", Order: order})
}

// LogCancel durably records an order cancellation (write + fsync).
func (w *WAL) LogCancel(symbol string, orderID uint64) error {
	return w.append(WALEntry{Action: "CANCEL", Symbol: symbol, OrderID: orderID})
}

// append writes one entry and fsyncs it while holding the WAL lock, so no other
// entry can interleave. The entry is durable only if this returns nil. On failure
// the file is rolled back to its previous length so no partial or unacknowledged
// record is left behind.
func (w *WAL) append(entry WALEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("%w: encode: %w", ErrWAL, err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.broken != nil {
		return fmt.Errorf("%w: log disabled after earlier failure: %w", ErrWAL, w.broken)
	}

	start := w.size
	if _, err := w.file.Write(data); err != nil {
		if rbErr := w.rollback(start); rbErr != nil {
			w.broken = fmt.Errorf("write failed (%v) and rollback failed: %w", err, rbErr)
		}
		return fmt.Errorf("%w: write: %w", ErrWAL, err)
	}

	if err := w.file.Sync(); err != nil {
		// After a failed fsync the on-disk state is unknown (the kernel may already have
		// dropped the dirty pages). Roll back best-effort and stop accepting writes; the
		// process must restart and recover from what is actually on disk.
		_ = w.rollback(start)
		w.broken = fmt.Errorf("fsync failed: %w", err)
		return fmt.Errorf("%w: fsync: %w", ErrWAL, err)
	}

	w.size = start + int64(len(data))
	return nil
}

func (w *WAL) rollback(offset int64) error {
	if err := w.file.Truncate(offset); err != nil {
		return err
	}
	if _, err := w.file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	return w.file.Sync()
}

// Recover replays the log into eng.
//   - An incomplete final record (no trailing newline) was never fsynced or acknowledged: it is dropped.
//   - A corrupt final record is dropped.
//   - A corrupt record with more data after it is NOT truncated: Recover returns an error,
//     because truncating would destroy acknowledged records. The caller must refuse to start.
func (w *WAL) Recover(eng *Engine) (uint64, error) {
	w.mu.Lock()

	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		w.mu.Unlock()
		return 0, err
	}

	reader := bufio.NewReader(w.file)
	var validOffset int64
	var maxOrderID uint64
	var entries []WALEntry

	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			w.mu.Unlock()
			return maxOrderID, fmt.Errorf("WAL read error at offset %d: %w", validOffset, readErr)
		}
		if len(line) == 0 {
			break // clean end of file
		}
		if line[len(line)-1] != '\n' {
			log.Printf("WAL recovery: dropping incomplete final record at offset %d (%d bytes)", validOffset, len(line))
			break
		}

		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) > 0 {
			var entry WALEntry
			err := json.Unmarshal(trimmed, &entry)
			if err == nil {
				err = entry.validate()
			}
			if err != nil {
				remaining, peekErr := io.ReadAll(reader)
				if peekErr != nil {
					w.mu.Unlock()
					return maxOrderID, fmt.Errorf("WAL read error at offset %d while verifying tail corruption: %w", validOffset, peekErr)
				}
				if len(bytes.TrimSpace(remaining)) > 0 {
					w.mu.Unlock()
					return maxOrderID, fmt.Errorf("WAL corrupt at offset %d with more records after it; refusing to truncate acknowledged data: %w", validOffset, err)
				}
				log.Printf("WAL recovery: dropping corrupt final record at offset %d: %v", validOffset, err)
				break
			}
			entries = append(entries, entry)
		}
		validOffset += int64(len(line))
	}

	if err := w.file.Truncate(validOffset); err != nil {
		w.mu.Unlock()
		return maxOrderID, fmt.Errorf("failed to truncate WAL tail: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		w.mu.Unlock()
		return maxOrderID, fmt.Errorf("failed to sync WAL after truncation: %w", err)
	}
	if _, err := w.file.Seek(validOffset, io.SeekStart); err != nil {
		w.mu.Unlock()
		return maxOrderID, fmt.Errorf("failed to seek to WAL tail: %w", err)
	}
	w.size = validOffset
	w.mu.Unlock()

	// Replay entries into eng outside w.mu.
	// This avoids any lock-order inversion between w.mu and engine book mutexes.
	for _, entry := range entries {
		switch entry.Action {
		case "PLACE":
			eng.RegisterSymbol(entry.Order.Symbol)
			if entry.Order.ID > maxOrderID {
				maxOrderID = entry.Order.ID
			}
			if _, err := eng.ProcessOrder(entry.Order); err != nil {
				return maxOrderID, fmt.Errorf("WAL recovery: failed replaying order %d: %w", entry.Order.ID, err)
			}
		case "CANCEL":
			if entry.OrderID > maxOrderID {
				maxOrderID = entry.OrderID
			}
			if _, err := eng.CancelOrder(entry.Symbol, entry.OrderID); err != nil {
				return maxOrderID, fmt.Errorf("WAL recovery: failed replaying cancel for order %d: %w", entry.OrderID, err)
			}
		}
	}

	eng.SetMinOrderID(maxOrderID)
	return maxOrderID, nil
}

// Sync commits the current contents of the WAL file to stable storage.
// (LogPlace/LogCancel already fsync; this is kept for API compatibility.)
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Sync()
}

// Close closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
