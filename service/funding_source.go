package service

// FundingSource identifies the selected source of API request quota. All
// mutations are performed by the durable billing reservation state machine;
// implementations deliberately expose no standalone debit/refund methods.
type FundingSource interface {
	Source() string
}

type WalletFunding struct{}

func (w *WalletFunding) Source() string { return BillingSourceWallet }

type SubscriptionFunding struct {
	subscriptionId  int
	preConsumed     int64
	AmountTotal     int64
	AmountUsedAfter int64
	PlanId          int
	PlanTitle       string
}

func (s *SubscriptionFunding) Source() string { return BillingSourceSubscription }
