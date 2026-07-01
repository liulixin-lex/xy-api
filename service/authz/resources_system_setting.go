package authz

const (
	ResourceSystemSetting = "system_setting"

	ActionManage = "manage"
)

var SystemSettingManage = Permission{Resource: ResourceSystemSetting, Action: ActionManage}

func init() {
	RegisterResource(ResourceDefinition{
		Resource: ResourceSystemSetting,
		LabelKey: "System Settings",
		Actions: []ActionDefinition{
			{
				Action:         ActionManage,
				LabelKey:       "Manage system settings",
				DescriptionKey: "View and update global system configuration.",
			},
		},
	})
}
