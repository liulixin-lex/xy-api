package model

import "gorm.io/gorm"

// HasStripeAccountBoundData reports whether any durable record still depends
// on the configured Stripe platform or connected account. It intentionally
// includes pre-canonical inventory so an upgraded installation cannot switch
// accounts merely because its historical rows predate PaymentOrder.
func HasStripeAccountBoundData() (bool, error) {
	return hasStripeAccountBoundData(DB)
}

func hasStripeAccountBoundData(db *gorm.DB) (bool, error) {
	if db == nil {
		db = DB
	}
	checks := []struct {
		model any
		query func(*gorm.DB) *gorm.DB
	}{
		{model: &PaymentOrder{}, query: func(db *gorm.DB) *gorm.DB { return db.Where("provider = ?", PaymentProviderStripe) }},
		{model: &PaymentEvent{}, query: func(db *gorm.DB) *gorm.DB {
			return db.Where("provider = ? AND (payment_order_id > 0 OR paid = ? OR refunded = ? OR disputed = ? OR dispute_resolved = ? OR manual_review = ? OR paid_amount_minor > 0 OR refunded_amount_minor > 0 OR disputed_amount_minor > 0 OR customer_id <> ? OR provider_payment_key <> ?)",
				PaymentProviderStripe, true, true, true, true, true, "", "")
		}},
		{model: &TopUp{}, query: func(db *gorm.DB) *gorm.DB {
			return db.Where("payment_provider = ? OR payment_method = ?", PaymentProviderStripe, PaymentMethodStripe)
		}},
		{model: &SubscriptionOrder{}, query: func(db *gorm.DB) *gorm.DB {
			return db.Where("payment_provider = ? OR payment_method = ?", PaymentProviderStripe, PaymentMethodStripe)
		}},
		{model: &StripeLegacySubscription{}, query: func(db *gorm.DB) *gorm.DB { return db }},
		{model: &StripeLegacyInvoice{}, query: func(db *gorm.DB) *gorm.DB { return db }},
		{model: &PaymentCustomerBinding{}, query: func(db *gorm.DB) *gorm.DB { return db.Where("provider = ?", PaymentProviderStripe) }},
		{model: &PaymentCustomerBindingRetirement{}, query: func(db *gorm.DB) *gorm.DB { return db.Where("provider = ?", PaymentProviderStripe) }},
		{model: &User{}, query: func(db *gorm.DB) *gorm.DB { return db.Where("stripe_customer <> ?", "") }},
	}
	for _, check := range checks {
		if !db.Migrator().HasTable(check.model) {
			continue
		}
		var count int64
		if err := check.query(db.Model(check.model)).Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}
