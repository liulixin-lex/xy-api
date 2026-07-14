package authz

const (
	ResourceBillingReview = "billing_review"

	ActionBillingReviewRead    = "read"
	ActionBillingReviewResolve = "resolve"
)

var (
	BillingReviewRead    = Permission{Resource: ResourceBillingReview, Action: ActionBillingReviewRead}
	BillingReviewResolve = Permission{Resource: ResourceBillingReview, Action: ActionBillingReviewResolve}
)

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceBillingReview,
		LabelKey: "Billing review",
		Actions: []ActionDefinition{
			{
				Action:         ActionBillingReviewRead,
				LabelKey:       "View billing reviews",
				DescriptionKey: "View ambiguous asynchronous billing cases and their financial consequences.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionBillingReviewResolve,
				LabelKey:       "Resolve billing reviews",
				DescriptionKey: "Approve or reject an asynchronous billing reconciliation decision.",
			},
		},
	})
}
