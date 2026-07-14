package authz

const (
	ResourceBillingProjectionOps = "billing_projection_ops"

	ActionBillingProjectionRead    = "read"
	ActionBillingProjectionRequeue = "requeue"
	ActionBillingProjectionResolve = "resolve"
)

var (
	BillingProjectionRead = Permission{
		Resource: ResourceBillingProjectionOps,
		Action:   ActionBillingProjectionRead,
	}
	BillingProjectionRequeue = Permission{
		Resource: ResourceBillingProjectionOps,
		Action:   ActionBillingProjectionRequeue,
	}
	BillingProjectionResolve = Permission{
		Resource: ResourceBillingProjectionOps,
		Action:   ActionBillingProjectionResolve,
	}
)

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceBillingProjectionOps,
		LabelKey: "Billing projection operations",
		Actions: []ActionDefinition{
			{
				Action:         ActionBillingProjectionRead,
				LabelKey:       "View billing projection failures",
				DescriptionKey: "View failed billing projections and quarantined log sink conflicts without sensitive payloads.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionBillingProjectionRequeue,
				LabelKey:       "Requeue billing projections",
				DescriptionKey: "Requeue a failed billing projection after its failure has been reviewed.",
			},
			{
				Action:         ActionBillingProjectionResolve,
				LabelKey:       "Resolve billing log conflicts",
				DescriptionKey: "Verify a quarantined billing log conflict and requeue its frozen projection.",
			},
		},
	})
}
