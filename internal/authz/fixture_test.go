package authz

import "github.com/igrybkov/ha-guest-access/internal/model"

// house is the inventory most tests run against.
//
// Guest room holds a ceiling light, a lamp whose device sits in the office
// but which has its own area, a fan through its device, a lock, and some
// entities expansion must skip. The bedside light's device is in the guest
// room, but the light itself is in the office.
func house() model.Inventory {
	ents := []model.Entity{
		{ID: "light.guest_ceiling", AreaID: "guest_room"},
		{ID: "light.guest_lamp", DeviceID: "dev_lamp", AreaID: "guest_room"},
		{ID: "light.bedside", DeviceID: "dev_bed", AreaID: "office"},
		{ID: "light.office", AreaID: "office"},
		{ID: "light.guest_hidden", AreaID: "guest_room", Hidden: true},
		{ID: "light.guest_disabled", AreaID: "guest_room", Disabled: true},
		{ID: "switch.guest_config", AreaID: "guest_room", EntityCategory: "config"},
		{ID: "lock.guest_room_door", AreaID: "guest_room"},
		{ID: "lock.front_door"},
		{ID: "fan.guest", DeviceID: "dev_fan"},
		{ID: "sensor.guest_fan_rssi", DeviceID: "dev_fan", EntityCategory: "diagnostic"},
		{ID: "media_player.living", DeviceID: "dev_sonos"},
		{ID: "media_player.office", DeviceID: "dev_office"},
		{ID: "media_player.tagged", DeviceID: "dev_tagged"},
		{ID: "media_player.direct", Labels: []string{"guest_music"}},
		{ID: "media_player.private", Labels: []string{"guest_music", "private"}},
		{ID: "media_player.config_tagged", Labels: []string{"guest_music"}, EntityCategory: "config"},
		{ID: "media_player.hidden_tagged", Labels: []string{"guest_music"}, Hidden: true},
		{ID: "media_player.living_tv", AreaID: "living_room"},
		{ID: "script.let_me_in", Labels: []string{"guest_script"}},
		{ID: "script.im_leaving", Labels: []string{"guest_script"}},
		{ID: "script.secret", Labels: []string{"guest_script"}, Hidden: true},
	}
	inv := model.Inventory{
		Entities: map[string]model.Entity{},
		Devices: map[string]model.Device{
			"dev_lamp":   {ID: "dev_lamp", Name: "Lamp", AreaID: "office"},
			"dev_bed":    {ID: "dev_bed", Name: "Bed", AreaID: "guest_room"},
			"dev_fan":    {ID: "dev_fan", Name: "Guest Fan", AreaID: "guest_room"},
			"dev_sonos":  {ID: "dev_sonos", Name: "Sonos One", NameByUser: "Living Sonos", AreaID: "living_room"},
			"dev_office": {ID: "dev_office", Name: "Office Speaker", AreaID: "office"},
			"dev_tagged": {ID: "dev_tagged", Name: "Tagged", AreaID: "office", Labels: []string{"guest_music"}},
		},
		Areas: map[string]model.Area{
			"guest_room":  {ID: "guest_room", Name: "Guest room"},
			"living_room": {ID: "living_room", Name: "Living room", Labels: []string{"guest_area"}},
			"office":      {ID: "office", Name: "Office"},
		},
		Labels: map[string]model.Label{
			"guest_music":  {ID: "guest_music", Name: "Guest music"},
			"guest_script": {ID: "guest_script", Name: "Guest script"},
			"guest_area":   {ID: "guest_area", Name: "Guest area"},
			"private":      {ID: "private", Name: "Private"},
		},
	}
	for _, e := range ents {
		inv.Entities[e.ID] = e
	}
	return inv
}

func local() *model.Access { return &model.Access{Network: model.NetworkLocal} }

// guestTemplates is the template set the Check tests grant from.
func guestTemplates() []model.Template {
	return []model.Template{
		{ID: "room", Name: "Guest room", Rules: []model.Rule{{
			Select:  model.Selector{Areas: []string{"guest_room"}},
			Actions: []string{"light.turn_on", "light.turn_off", "fan.set_percentage"},
		}}},
		{ID: "entry", Name: "Entry", Access: &model.Access{NearHomeM: 300}, Rules: []model.Rule{
			{Select: model.Selector{Entities: []string{"lock.front_door"}}, Actions: []string{"lock.unlock"}},
			{
				Select:    model.Selector{Entities: []string{"script.let_me_in"}},
				Actions:   []string{"script.run", "script.cancel"},
				Variables: map[string]any{"source": "guest_access", "nested": map[string]any{"a": []any{"b"}}},
			},
		}},
		{ID: "music", Name: "Music", Access: local(), Rules: []model.Rule{{
			Select:  model.Selector{Entities: []string{"media_player.living"}},
			Actions: []string{"media_player.volume_set", "media_player.volume_mute"},
		}}},
	}
}

func guestLink() model.Link {
	return model.Link{
		Name:        "Anna",
		TemplateIDs: []string{"room", "entry", "music"},
		Extra: []model.Rule{{
			Select:  model.Selector{Entities: []string{"light.guest_*"}},
			Actions: []string{"light.toggle"},
		}},
	}
}

// localOff is the AccessFunc while local access is turned off in
// settings: a local rule denies, everything else passes.
func localOff(a model.Access) bool { return !a.Local() }

func allowAll(model.Access) bool { return true }
