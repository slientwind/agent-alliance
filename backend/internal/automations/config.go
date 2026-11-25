package automations

import (
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"alliance/internal/llmgateway"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/robfig/cron"
	"go.temporal.io/sdk/client"
)

type config struct {
	Title        string          `json:"title"`
	Goal         string          `json:"goal"`
	Team         string          `json:"team"`
	Tier         llmgateway.Tier `json:"tier"`
	Zone         string          `json:"zone"`
	Cron         *string         `json:"cron"`
	OnceAt       *time.Time      `json:"onceAt"`
	Expected     int             `json:"expected"`
	NotifyFeishu *bool           `json:"notifyFeishu"`
	NotifyEmail  *bool           `json:"notifyEmail"`
}

func (c *config) validate(now time.Time) error {
	c.Title, c.Goal = strings.TrimSpace(c.Title), strings.TrimSpace(c.Goal)
	if c.Title == "" || c.Goal == "" || len(c.Goal) > 16<<10 || utf8.RuneCountInString(c.Title) > 120 ||
		strings.ContainsRune(c.Title+c.Goal, 0) || (c.Cron == nil) == (c.OnceAt == nil) {
		return web.Fail(422, "请填写任务名称、工作目标和执行时间")
	}
	if _, err := uuid.Parse(c.Team); err != nil {
		return web.Fail(422, "请选择工作团队")
	}
	if c.Tier == "" {
		c.Tier = llmgateway.Default
	}
	if c.Tier != llmgateway.Default && c.Tier != llmgateway.Fast && c.Tier != llmgateway.Reasoning {
		return web.Fail(422, "请选择可用的模型档位")
	}
	if c.Zone == "" || c.Zone == "Local" || len(c.Zone) > 128 {
		return web.Fail(422, "请选择有效时区")
	}
	if _, err := time.LoadLocation(c.Zone); err != nil {
		return web.Fail(422, "请选择有效时区")
	}
	if c.OnceAt != nil {
		t := c.OnceAt.UTC().Truncate(time.Second)
		if !t.After(now) || t.Year() > 9999 {
			return web.Fail(422, "请选择将来的执行时间")
		}
		c.OnceAt = &t
		return nil
	}
	fields := strings.Fields(*c.Cron)
	expression := strings.Join(fields, " ")
	if len(fields) != 5 || len(expression) > 120 {
		return web.Fail(422, "请输入五段式定时表达式")
	}
	if _, err := cron.ParseStandard(expression); err != nil {
		return web.Fail(422, "请核对定时表达式")
	}
	c.Cron = &expression
	return nil
}

func (c config) spec() client.ScheduleSpec {
	if c.Cron != nil {
		return client.ScheduleSpec{CronExpressions: []string{*c.Cron}, TimeZoneName: c.Zone}
	}
	at := c.OnceAt.UTC()
	field := func(n int) []client.ScheduleRange { return []client.ScheduleRange{{Start: n, End: n}} }
	return client.ScheduleSpec{TimeZoneName: "UTC", Calendars: []client.ScheduleCalendarSpec{{
		Year: field(at.Year()), Month: field(int(at.Month())), DayOfMonth: field(at.Day()),
		Hour: field(at.Hour()), Minute: field(at.Minute()), Second: field(at.Second()),
	}}}
}
