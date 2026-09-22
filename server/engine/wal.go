package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// WALEntry represents a single logged event in the Write-Ahead Log
type WALEntry struct {
	Action  string `json:"action"` // "PLACE" or "CANCEL"
	Order   *Order `json:"order,omitempty"`
	OrderID uint64 `json:"order_id,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
}

// WAL manages the append-only log file on disk
type WAL struct {
	file *os.File
	mu   sync.Mutex
}

// OpenWAL opens or creates the WAL log file
func OpenWAL(path string) (*WAL, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	return &WAL{file: file}, nil
}

// LogPlace writes an order placement event to the log
func (w *WAL) LogPlace(order *Order) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := WALEntry{
		Action: "PLACE",
		Order:  order,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = w.file.Write(append(data, '\n'))
	return err
}

// LogCancel writes an order cancellation event to the log
func (w *WAL) LogCancel(symbol string, orderID uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := WALEntry{
		Action:  "CANCEL",
		Symbol:  symbol,
		OrderID: orderID,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = w.file.Write(append(data, '\n'))
	return err
}

// Recover reads all valid entries from the log, replays them into the engine,
// and truncates any partially-written corrupt tail left from an unclean mid-write crash.
func (w *WAL) Recover(eng *Engine) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.file.Seek(0, 0); err != nil {
		return 0, err
	}

	reader := bufio.NewReader(w.file)
	var validOffset int64 = 0
	var maxOrderID uint64 = 0

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			if len(trimmed) > 0 {
				var entry WALEntry
				if err := json.Unmarshal(trimmed, &entry); err != nil {
					log.Printf("WAL recovery: detected corrupt/truncated entry at offset %d: %v. Truncating file tail to clean state.", validOffset, err)
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

		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("WAL recovery: error reading log: %v", readErr)
			}
			break
		}
	}

	// Truncate any corrupt/partial write at the tail so future appends start clean
	if err := w.file.Truncate(validOffset); err != nil {
		return maxOrderID, fmt.Errorf("failed to truncate corrupt WAL tail: %w", err)
	}
	if _, err := w.file.Seek(validOffset, 0); err != nil {
		return maxOrderID, fmt.Errorf("failed to seek to clean WAL tail: %w", err)
	}

	eng.SetMinOrderID(maxOrderID + 1)
	return maxOrderID, nil
}

// Sync commits the current contents of the WAL file to stable disk storage
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Sync()
}

// Close closes the WAL file
func (w *WAL) Close() error {
	return w.file.Close()
}
