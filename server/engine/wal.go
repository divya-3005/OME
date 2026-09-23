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

// OpenWAL opens or creates the WAL log file.
func OpenWAL(path string) (*WAL, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
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
	return w.file.Sync()
}

// Recover replays the log into eng.
//   - An incomplete final record (no trailing newline) was never fsynced or acknowledged: it is dropped.
//   - A corrupt final record is dropped.
//   - A corrupt record with more data after it is NOT truncated: Recover returns an error,
//     because truncating would destroy acknowledged records. The caller must refuse to start.
func (w *WAL) Recover(eng *Engine) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}

	reader := bufio.NewReader(w.file)
	var validOffset int64
	var maxOrderID uint64

	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
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
				if _, peekErr := reader.Peek(1); peekErr == nil {
					return maxOrderID, fmt.Errorf("WAL corrupt at offset %d with more records after it; refusing to truncate acknowledged data: %w", validOffset, err)
				}
				log.Printf("WAL recovery: dropping corrupt final record at offset %d: %v", validOffset, err)
				break
			}

			switch entry.Action {
			case "PLACE":
				eng.RegisterSymbol(entry.Order.Symbol)
				if entry.Order.ID > maxOrderID {
					maxOrderID = entry.Order.ID
				}
				if _, err := eng.ProcessOrder(entry.Order); err != nil {
					log.Printf("WAL recovery: warning replaying order %d: %v", entry.Order.ID, err)
				}
			case "CANCEL":
				if entry.OrderID > maxOrderID {
					maxOrderID = entry.OrderID
				}
				if _, err := eng.CancelOrder(entry.Symbol, entry.OrderID); err != nil {
					log.Printf("WAL recovery: warning replaying cancel for order %d: %v", entry.OrderID, err)
				}
			}
		}
		validOffset += int64(len(line))
	}

	if err := w.file.Truncate(validOffset); err != nil {
		return maxOrderID, fmt.Errorf("failed to truncate WAL tail: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return maxOrderID, fmt.Errorf("failed to sync WAL after truncation: %w", err)
	}
	if _, err := w.file.Seek(validOffset, io.SeekStart); err != nil {
		return maxOrderID, fmt.Errorf("failed to seek to WAL tail: %w", err)
	}
	w.size = validOffset

	eng.SetMinOrderID(maxOrderID + 1)
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
