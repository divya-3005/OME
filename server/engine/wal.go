package engine

import (
	"bufio"
	"encoding/json"
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

// Recover reads all entries from the log and replays them into the engine
func (w *WAL) Recover(eng *Engine) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.file.Seek(0, 0); err != nil {
		return err
	}

	scanner := bufio.NewScanner(w.file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry WALEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return err
		}

		switch entry.Action {
		case "PLACE":
			eng.RegisterSymbol(entry.Order.Symbol)
			eng.ProcessOrder(entry.Order)
		case "CANCEL":
			eng.CancelOrder(entry.Symbol, entry.OrderID)
		}
	}

	return scanner.Err()
}

// Close closes the WAL file
func (w *WAL) Close() error {
	return w.file.Close()
}
