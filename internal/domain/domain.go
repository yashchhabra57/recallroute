// Package domain holds the core types shared by the matcher, workflow,
// store and API layers.
package domain

import (
	"time"

	"github.com/yashchhabra57/recallroute/internal/lotcode"
)

// Recall is one product entry from a recall feed (openFDA calls this an
// enforcement report). A single recall event can cover many products;
// each product has its own ID.
type Recall struct {
	ID             string       `json:"id"` // e.g. F-0123-2026
	EventID        string       `json:"event_id,omitempty"`
	Firm           string       `json:"firm"`
	Product        string       `json:"product"`
	Reason         string       `json:"reason"`
	Classification string       `json:"classification"` // Class I, II, III
	Status         string       `json:"status"`         // Ongoing, Completed, Terminated
	Distribution   string       `json:"distribution,omitempty"`
	CodeInfo       string       `json:"code_info"`
	GTINs          []string     `json:"gtins"`
	GTINsInferred  bool         `json:"gtins_inferred,omitempty"`
	Spec           lotcode.Spec `json:"spec"`
	InitiatedOn    time.Time    `json:"initiated_on"`
	ReportedOn     time.Time    `json:"reported_on"`
	Source         string       `json:"source"`
}

// Active reports whether the recall can still produce alerts.
func (r Recall) Active() bool { return r.Status != "Terminated" }

// Lot is a quantity of one product on the shelf with one lot code.
type Lot struct {
	ID         int64     `json:"id"`
	SKU        string    `json:"sku"`
	Name       string    `json:"name"`
	Brand      string    `json:"brand"`
	GTIN       string    `json:"gtin"` // normalized 14 digits, or "" if unknown
	LotCode    string    `json:"lot_code"`
	BestBy     time.Time `json:"best_by"` // zero if unknown
	Quantity   int       `json:"quantity"`
	Location   string    `json:"location"`
	ReceivedOn time.Time `json:"received_on"`
}

// Tier says how sure the matcher is that a lot is recalled.
type Tier string

const (
	// TierConfirmed: barcode and lot/date both match the notice.
	TierConfirmed Tier = "confirmed"
	// TierProbable: barcode matches but the notice's lot info is missing,
	// unparseable, or the lot record lacks the field it needs.
	TierProbable Tier = "probable"
	// TierReview: the notice has no barcode; the product name and brand
	// look similar. A person must confirm before pulling.
	TierReview Tier = "review"
)

// State is a step in the pull-and-notify workflow.
type State string

const (
	StateNew          State = "new"
	StateAcknowledged State = "acknowledged"
	StatePulled       State = "pulled"
	StateNotified     State = "notified"
	StateClosed       State = "closed"
	StateDismissed    State = "dismissed"
)

// Alert links one recall to one inventory lot and carries it through the
// workflow.
type Alert struct {
	ID        int64     `json:"id"`
	RecallID  string    `json:"recall_id"`
	LotID     int64     `json:"lot_id"`
	Tier      Tier      `json:"tier"`
	Score     float64   `json:"score"`
	Reasons   []string  `json:"reasons"`
	State     State     `json:"state"`
	Version   int       `json:"version"`
	PulledQty int       `json:"pulled_qty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Event is one row of an alert's audit trail.
type Event struct {
	ID         int64     `json:"id"`
	AlertID    int64     `json:"alert_id"`
	Action     string    `json:"action"`
	From       State     `json:"from"`
	To         State     `json:"to"`
	Actor      string    `json:"actor"`
	Note       string    `json:"note,omitempty"`
	Quantity   int       `json:"quantity,omitempty"`
	Recipients []string  `json:"recipients,omitempty"`
	At         time.Time `json:"at"`
}
