package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/task"
)

type taskAssignmentRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
	Accept           *bool `json:"accept"           binding:"required"`
}

type taskScheduleRequest struct {
	ExpectedRevision int64             `json:"expectedRevision" binding:"revision"`
	Trigger          *schedule.Trigger `json:"trigger"`
	Paused           bool              `json:"paused"`
}

type taskOccurrenceRequest struct {
	ExpectedRevision int64      `json:"expectedRevision" binding:"revision"`
	OccurrenceAt     time.Time  `json:"occurrenceAt"     binding:"required"`
	RunAt            *time.Time `json:"runAt"`
	Cancel           bool       `json:"cancel"`
	Title            *string    `json:"title"            binding:"omitempty,max=200"`
	Description      *string    `json:"description"      binding:"omitempty,max=50000"`
}

func (h *Handler) registerTaskCalendarRoutes(routes *gin.RouterGroup) {
	if h.TaskCalendarService == nil {
		return
	}
	human := middleware.RequireHumanActor()
	routes.GET("/calendar/tasks", human, h.listCalendarTasks)
	routes.GET("/tasks/:taskId/schedule", human, h.getTaskSchedule)
	routes.PUT("/tasks/:taskId/schedule", human, h.updateTaskSchedule)
	routes.POST("/tasks/:taskId/schedule/occurrences", human, h.updateTaskOccurrence)
	routes.POST("/tasks/:taskId/assignment", human, h.decideTaskAssignment)
}

func (h *Handler) listCalendarTasks(c *gin.Context) {
	p, ok := taskPrincipal(c)
	if !ok {
		return
	}
	from, err := time.Parse(time.RFC3339, c.Query("from"))
	if err != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	to, err := time.Parse(time.RFC3339, c.Query("to"))
	if err != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	result, err := h.TaskCalendarService.ListOccurrences(c.Request.Context(), p, from, to)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, taskCalendarPageToResponse(result))
}

func (h *Handler) getTaskSchedule(c *gin.Context) {
	p, id, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	result, err := h.TaskCalendarService.GetSchedule(c.Request.Context(), p, id)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, taskCalendarScheduleToResponse(result))
}

func (h *Handler) updateTaskSchedule(c *gin.Context) {
	request, err := bindRequest[taskScheduleRequest](c, maxTaskBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, id, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	result, err := h.TaskCalendarService.UpdateSchedule(
		c.Request.Context(),
		p,
		id,
		task.CalendarScheduleChange{
			ExpectedRevision: request.ExpectedRevision,
			Trigger:          request.Trigger,
			Paused:           request.Paused,
		},
	)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, task.NewMutationResponse(result))
}

func (h *Handler) updateTaskOccurrence(c *gin.Context) {
	request, err := bindRequest[taskOccurrenceRequest](c, maxTaskBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, id, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	result, err := h.TaskCalendarService.UpdateOccurrence(
		c.Request.Context(),
		p,
		id,
		task.CalendarOccurrenceChange{
			ExpectedRevision: request.ExpectedRevision,
			OccurrenceAt:     request.OccurrenceAt,
			RunAt:            request.RunAt,
			Cancel:           request.Cancel,
			Title:            request.Title,
			Description:      request.Description,
		},
	)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, task.NewMutationResponse(result))
}

func (h *Handler) decideTaskAssignment(c *gin.Context) {
	request, err := bindRequest[taskAssignmentRequest](c, maxTaskBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	p, id, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	result, err := h.TaskCalendarService.DecideAssignment(
		c.Request.Context(),
		p,
		id,
		request.ExpectedRevision,
		*request.Accept,
	)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, task.NewMutationResponse(result))
}

type taskCalendarScheduleResponse struct {
	ID             uuid.UUID               `json:"id"`
	TemplateTaskID uuid.UUID               `json:"templateTaskId"`
	Trigger        scheduleTriggerResponse `json:"trigger"`
	Paused         bool                    `json:"paused"`
	Revision       int64                   `json:"revision"`
}

func taskCalendarScheduleToResponse(value task.CalendarSchedule) taskCalendarScheduleResponse {
	return taskCalendarScheduleResponse{
		ID:             value.ID,
		TemplateTaskID: value.TemplateTaskID,
		Trigger:        scheduleTriggerToResponse(value.Trigger),
		Paused:         value.Paused,
		Revision:       value.Revision,
	}
}

type taskCalendarOccurrenceResponse struct {
	Task         task.Response                `json:"task"`
	Schedule     taskCalendarScheduleResponse `json:"schedule"`
	OccurrenceAt time.Time                    `json:"occurrenceAt"`
	RunAt        time.Time                    `json:"runAt"`
	Materialized bool                         `json:"materialized"`
}

type taskCalendarPageResponse struct {
	Occurrences []taskCalendarOccurrenceResponse `json:"occurrences"`
	Truncated   bool                             `json:"truncated"`
}

func taskCalendarPageToResponse(value task.CalendarPage) taskCalendarPageResponse {
	result := taskCalendarPageResponse{
		Occurrences: make([]taskCalendarOccurrenceResponse, 0, len(value.Occurrences)),
		Truncated:   value.Truncated,
	}
	for _, item := range value.Occurrences {
		result.Occurrences = append(
			result.Occurrences,
			taskCalendarOccurrenceResponse{
				Task:         item.Task,
				Schedule:     taskCalendarScheduleToResponse(item.Schedule),
				OccurrenceAt: item.OccurrenceAt,
				RunAt:        item.RunAt,
				Materialized: item.Materialized,
			},
		)
	}
	return result
}
