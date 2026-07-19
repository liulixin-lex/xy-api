package authz

const ResourcePaymentGateway = "payment_gateway"

var PaymentGatewayManage = Permission{Resource: ResourcePaymentGateway, Action: ActionManage}

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourcePaymentGateway,
		LabelKey: "Payment Gateway",
		Actions: []ActionDefinition{
			{
				Action:         ActionManage,
				LabelKey:       "Manage payment gateway settings",
				DescriptionKey: "Configure payment gateway credentials, callbacks, and payment availability.",
			},
		},
	})
}
