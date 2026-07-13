package authz

const (
	ResourceChannelRouting = "channel_routing"

	ActionDeploy      = "deploy"
	ActionAuditExport = "audit_export"
)

var (
	ChannelRoutingRead           = Permission{Resource: ResourceChannelRouting, Action: ActionRead}
	ChannelRoutingOperate        = Permission{Resource: ResourceChannelRouting, Action: ActionOperate}
	ChannelRoutingWrite          = Permission{Resource: ResourceChannelRouting, Action: ActionWrite}
	ChannelRoutingDeploy         = Permission{Resource: ResourceChannelRouting, Action: ActionDeploy}
	ChannelRoutingSensitiveWrite = Permission{Resource: ResourceChannelRouting, Action: ActionSensitiveWrite}
	ChannelRoutingAuditExport    = Permission{Resource: ResourceChannelRouting, Action: ActionAuditExport}
)

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceChannelRouting,
		LabelKey: "Channel routing",
		Actions: []ActionDefinition{
			{
				Action:         ActionRead,
				LabelKey:       "Read channel routing",
				DescriptionKey: "View channel routing health, decisions, costs, policies, and operations.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionOperate,
				LabelKey:       "Operate channel routing",
				DescriptionKey: "Run probes, synchronize costs, and reset runtime routing state.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionWrite,
				LabelKey:       "Edit channel routing policies",
				DescriptionKey: "Create, edit, validate, and simulate channel routing policy drafts.",
				DefaultRoles:   []string{BuiltInRoleAdmin},
			},
			{
				Action:         ActionDeploy,
				LabelKey:       "Deploy channel routing policies",
				DescriptionKey: "Approve, publish, promote, and roll back channel routing policies.",
			},
			{
				Action:         ActionSensitiveWrite,
				LabelKey:       "Edit sensitive channel routing settings",
				DescriptionKey: "Edit upstream credentials, private egress rules, and other sensitive routing settings.",
			},
			{
				Action:         ActionAuditExport,
				LabelKey:       "Export channel routing audits",
				DescriptionKey: "Export bounded channel routing decision and policy audit data.",
			},
		},
	})
}
