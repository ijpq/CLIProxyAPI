package billing

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PaymentMethod identifies a top-up channel. The portal exposes the active
// methods to the user so the UI can show only what the operator configured.
type PaymentMethod struct {
	// Method is the stable channel identifier ("usdt", "wechat", "alipay", ...).
	Method string `json:"method"`
	// Network qualifies the channel when relevant (e.g. "TRC20", "ERC20").
	Network string `json:"network,omitempty"`
	// Currency is the unit the user pays in (UI display only).
	Currency string `json:"currency"`
	// WalletAddress / PaymentURL is what the user sends funds to. Exactly one
	// is expected to be non-empty per method.
	WalletAddress string `json:"wallet_address,omitempty"`
	PaymentURL    string `json:"payment_url,omitempty"`
	// QRCodeURL is the URL to a static personal QR code image the user scans
	// to pay (e.g. a wechat or alipay personal collection QR).
	QRCodeURL string `json:"qr_code_url,omitempty"`
	// RequiresUniqueAmount signals to the portal that each active order must
	// carry a unique total so the operator can match incoming payments by
	// amount alone (personal QR codes provide no transaction reference).
	RequiresUniqueAmount bool `json:"requires_unique_amount,omitempty"`
	// Notes is operator-supplied human-readable guidance ("send only USDT-TRC20").
	Notes string `json:"notes,omitempty"`
}

// TopupConfig holds the live top-up configuration.
type TopupConfig struct {
	mu        sync.RWMutex
	methods   map[string]PaymentMethod // key = method+":"+network (lowercased)
	orderTTL  time.Duration
	minAmount float64
}

// LoadTopupConfigFromEnv reads operator-supplied top-up settings from a small
// set of environment variables so the wallet address can be rotated without
// rebuilding the binary:
//
//	BILLING_TOPUP_NETWORKS    comma-separated list, currently only "usdt:<network>" entries
//	BILLING_USDT_TRC20        wallet address for USDT-TRC20
//	BILLING_USDT_ERC20        wallet address for USDT-ERC20
//	BILLING_USDT_BEP20        wallet address for USDT-BEP20
//	BILLING_TOPUP_NOTES       optional global notes shown to the user
//	BILLING_TOPUP_ORDER_TTL   pending order lifetime (e.g. "24h"); default 24h
//	BILLING_TOPUP_MIN_AMOUNT  minimum amount per order; default 1
//
// Returns an empty (non-nil) config when nothing is configured so the portal
// can report "no payment methods yet" instead of failing.
func LoadTopupConfigFromEnv() *TopupConfig {
	c := &TopupConfig{
		methods:   make(map[string]PaymentMethod),
		orderTTL:  24 * time.Hour,
		minAmount: 1,
	}

	addrs := map[string]string{
		"TRC20": strings.TrimSpace(os.Getenv("BILLING_USDT_TRC20")),
		"ERC20": strings.TrimSpace(os.Getenv("BILLING_USDT_ERC20")),
		"BEP20": strings.TrimSpace(os.Getenv("BILLING_USDT_BEP20")),
	}
	notes := strings.TrimSpace(os.Getenv("BILLING_TOPUP_NOTES"))
	for network, addr := range addrs {
		if addr == "" {
			continue
		}
		m := PaymentMethod{
			Method:        "usdt",
			Network:       network,
			Currency:      "USDT",
			WalletAddress: addr,
			Notes:         notes,
		}
		c.methods[methodKey(m.Method, m.Network)] = m
	}

	if wechatQR := strings.TrimSpace(os.Getenv("BILLING_WECHAT_QR_URL")); wechatQR != "" {
		m := PaymentMethod{
			Method:               "wechat",
			Currency:             "CNY",
			QRCodeURL:            wechatQR,
			RequiresUniqueAmount: true,
			Notes:                strings.TrimSpace(os.Getenv("BILLING_WECHAT_NOTES")),
		}
		c.methods[methodKey(m.Method, m.Network)] = m
	}
	if alipayQR := strings.TrimSpace(os.Getenv("BILLING_ALIPAY_QR_URL")); alipayQR != "" {
		m := PaymentMethod{
			Method:               "alipay",
			Currency:             "CNY",
			QRCodeURL:            alipayQR,
			RequiresUniqueAmount: true,
			Notes:                strings.TrimSpace(os.Getenv("BILLING_ALIPAY_NOTES")),
		}
		c.methods[methodKey(m.Method, m.Network)] = m
	}

	if raw := strings.TrimSpace(os.Getenv("BILLING_TOPUP_ORDER_TTL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			c.orderTTL = d
		}
	}
	if raw := strings.TrimSpace(os.Getenv("BILLING_TOPUP_MIN_AMOUNT")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			c.minAmount = v
		}
	}
	return c
}

// Methods returns a stable-ordered snapshot of the active payment methods.
func (c *TopupConfig) Methods() []PaymentMethod {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]PaymentMethod, 0, len(c.methods))
	for _, m := range c.methods {
		out = append(out, m)
	}
	return out
}

// Lookup returns the method for the requested method+network combination.
func (c *TopupConfig) Lookup(method, network string) (PaymentMethod, bool) {
	if c == nil {
		return PaymentMethod{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.methods[methodKey(method, network)]
	return m, ok
}

// OrderTTL returns the configured pending order lifetime.
func (c *TopupConfig) OrderTTL() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.orderTTL
}

// ValidateAmount enforces the minimum amount policy and returns a descriptive
// error suitable for surfacing to the API caller.
func (c *TopupConfig) ValidateAmount(amount float64) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}
	if amount < c.minAmount {
		return fmt.Errorf("amount must be at least %.2f", c.minAmount)
	}
	return nil
}

func methodKey(method, network string) string {
	return strings.ToLower(strings.TrimSpace(method)) + ":" + strings.ToUpper(strings.TrimSpace(network))
}
