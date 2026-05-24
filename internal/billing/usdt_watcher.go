package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// USDT-TRC20 contract address on the Tron mainnet. The watcher refuses to
// confirm any transfer that did not originate from this contract so a
// would-be attacker cannot trick the system by transferring a worthless
// look-alike TRC20 token with the same hash.
const usdtTRC20Contract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

// TopupConfirmer is the subset of the store used by the watcher: list
// outstanding orders and atomically credit a matching one.
type TopupConfirmer interface {
	ListSubmittedTopupOrders(ctx context.Context, method, network string, limit int) ([]store.TopupOrder, error)
	ConfirmTopupOrder(ctx context.Context, orderID, adminNote string) (store.TopupOrder, error)
}

// USDTTronWatcher polls TronGrid for incoming USDT-TRC20 transfers and
// confirms matching topup orders. It only acts on orders whose tx_hash has
// already been submitted by the user, so a misconfigured wallet address can
// never auto-credit unrelated transfers.
type USDTTronWatcher struct {
	store           TopupConfirmer
	walletAddress   string
	tronAPIBase     string
	tronAPIKey      string
	pollInterval    time.Duration
	amountTolerance float64
	onWalletChange  func(userID string)
	client          *http.Client
}

// USDTTronWatcherConfig groups watcher knobs. Empty fields keep their default.
type USDTTronWatcherConfig struct {
	WalletAddress   string
	TronAPIBase     string // default "https://api.trongrid.io"
	TronAPIKey      string // optional X-TRON-PRO-API-KEY value
	PollInterval    time.Duration
	AmountTolerance float64 // fractional tolerance, e.g. 0.005 = 0.5%
	OnWalletChange  func(userID string)
}

// NewUSDTTronWatcher constructs a watcher. The watcher is inert until Start
// is called; Start returns immediately and the polling runs in its own
// goroutine until ctx is cancelled.
func NewUSDTTronWatcher(store TopupConfirmer, cfg USDTTronWatcherConfig) *USDTTronWatcher {
	base := strings.TrimRight(strings.TrimSpace(cfg.TronAPIBase), "/")
	if base == "" {
		base = "https://api.trongrid.io"
	}
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	tolerance := cfg.AmountTolerance
	if tolerance < 0 {
		tolerance = 0
	}
	return &USDTTronWatcher{
		store:           store,
		walletAddress:   strings.TrimSpace(cfg.WalletAddress),
		tronAPIBase:     base,
		tronAPIKey:      strings.TrimSpace(cfg.TronAPIKey),
		pollInterval:    interval,
		amountTolerance: tolerance,
		onWalletChange:  cfg.OnWalletChange,
		// Per AGENTS.md: do not set per-request timeouts on user-facing
		// upstream traffic. The TronGrid client is an internal poller and
		// short timeouts here keep a stuck request from blocking the loop.
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Start launches the polling loop. Calling Start more than once on the same
// watcher will start additional loops; treat the watcher as a singleton.
func (w *USDTTronWatcher) Start(ctx context.Context) {
	if w == nil || w.store == nil || w.walletAddress == "" {
		return
	}
	go w.run(ctx)
}

func (w *USDTTronWatcher) run(ctx context.Context) {
	log.Infof("billing: USDT-TRC20 watcher started for %s (interval=%s)", w.walletAddress, w.pollInterval)
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	// Run one cycle immediately so a freshly-submitted order is picked up
	// without waiting for the first tick.
	w.cycle(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("billing: USDT-TRC20 watcher stopped")
			return
		case <-ticker.C:
			w.cycle(ctx)
		}
	}
}

func (w *USDTTronWatcher) cycle(ctx context.Context) {
	cycleCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	orders, err := w.store.ListSubmittedTopupOrders(cycleCtx, "usdt", "TRC20", 200)
	if err != nil {
		log.WithError(err).Error("billing: list submitted USDT orders failed")
		return
	}
	if len(orders) == 0 {
		return
	}

	transfers, err := w.fetchRecentTransfers(cycleCtx)
	if err != nil {
		log.WithError(err).Error("billing: TronGrid fetch failed")
		return
	}
	if len(transfers) == 0 {
		return
	}

	for _, order := range orders {
		w.tryConfirm(cycleCtx, order, transfers)
	}
}

// tronTransfer captures the fields we need from a TronGrid TRC20 transfer row.
type tronTransfer struct {
	TxHash         string
	To             string
	ContractAddr   string
	AmountInTokens float64
	Timestamp      time.Time
}

// fetchRecentTransfers calls the TronGrid TRC20-transfer endpoint scoped to
// our wallet, filtered by USDT contract and the past ~24h. Pagination is not
// followed; 200 rows per cycle is enough for a low-volume operator.
func (w *USDTTronWatcher) fetchRecentTransfers(ctx context.Context) (map[string]tronTransfer, error) {
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	url := fmt.Sprintf(
		"%s/v1/accounts/%s/transactions/trc20?only_to=true&contract_address=%s&min_timestamp=%d&limit=200",
		w.tronAPIBase, w.walletAddress, usdtTRC20Contract, since,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build trongrid request: %w", err)
	}
	if w.tronAPIKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", w.tronAPIKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trongrid GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trongrid GET status %d", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			TransactionID  string `json:"transaction_id"`
			BlockTimestamp int64  `json:"block_timestamp"`
			To             string `json:"to"`
			Value          string `json:"value"`
			Type           string `json:"type"`
			TokenInfo      struct {
				Address  string `json:"address"`
				Decimals int    `json:"decimals"`
			} `json:"token_info"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode trongrid response: %w", err)
	}

	out := make(map[string]tronTransfer, len(body.Data))
	for _, row := range body.Data {
		if !strings.EqualFold(row.Type, "Transfer") {
			continue
		}
		if !strings.EqualFold(row.TokenInfo.Address, usdtTRC20Contract) {
			continue
		}
		raw, err := strconv.ParseInt(row.Value, 10, 64)
		if err != nil || raw <= 0 {
			continue
		}
		decimals := row.TokenInfo.Decimals
		if decimals <= 0 {
			decimals = 6
		}
		amount := float64(raw) / pow10f(decimals)
		out[strings.ToLower(row.TransactionID)] = tronTransfer{
			TxHash:         row.TransactionID,
			To:             row.To,
			ContractAddr:   row.TokenInfo.Address,
			AmountInTokens: amount,
			Timestamp:      time.UnixMilli(row.BlockTimestamp),
		}
	}
	return out, nil
}

func (w *USDTTronWatcher) tryConfirm(ctx context.Context, order store.TopupOrder, transfers map[string]tronTransfer) {
	hashKey := strings.ToLower(strings.TrimSpace(order.TxHash))
	if hashKey == "" {
		return
	}
	transfer, ok := transfers[hashKey]
	if !ok {
		// Not visible on-chain yet (or beyond the 24h window). Leave it for
		// the next cycle or admin review.
		return
	}
	if !strings.EqualFold(transfer.To, order.WalletAddress) {
		log.Warnf("billing: tx %s recipient %s does not match order %s wallet %s",
			transfer.TxHash, transfer.To, order.ID, order.WalletAddress)
		return
	}
	expected, err := strconv.ParseFloat(strings.TrimSpace(order.Amount), 64)
	if err != nil || expected <= 0 {
		log.Warnf("billing: order %s has unparseable amount %q", order.ID, order.Amount)
		return
	}
	// Accept overpayment but reject material underpayment. Tolerance is a
	// fractional underrun allowance to absorb rounding when the user pays the
	// rounded display amount instead of the exact 6-decimal value.
	minAcceptable := expected * (1 - w.amountTolerance)
	if transfer.AmountInTokens < minAcceptable {
		log.Warnf("billing: tx %s amount %.6f below order %s expected %.6f", transfer.TxHash, transfer.AmountInTokens, order.ID, expected)
		return
	}

	note := fmt.Sprintf("auto-confirmed by watcher (paid=%.6f, block=%s)",
		transfer.AmountInTokens, transfer.Timestamp.Format(time.RFC3339))
	confirmed, err := w.store.ConfirmTopupOrder(ctx, order.ID, note)
	if err != nil {
		log.WithError(err).Errorf("billing: auto-confirm order %s failed", order.ID)
		return
	}
	log.Infof("billing: auto-confirmed order %s for user %s amount %.6f", confirmed.ID, confirmed.UserID, transfer.AmountInTokens)
	if w.onWalletChange != nil {
		w.onWalletChange(confirmed.UserID)
	}
}

func pow10f(n int) float64 {
	out := 1.0
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}
