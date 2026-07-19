package authz

const ResourcePaymentOperations = "payment_operations"

var PaymentOperationsManage = Permission{Resource: ResourcePaymentOperations, Action: ActionManage}

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourcePaymentOperations,
		LabelKey: "Payment Operations",
		Actions: []ActionDefinition{
			{
				Action:         ActionManage,
				LabelKey:       "Manage payment operations",
				DescriptionKey: "Review payment exceptions and apply audited financial resolutions.",
			},
		},
	})
}
