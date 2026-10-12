//go:build integration

package schedule

// Test-only fixture exports let lifecycle tests call the Agent and Company
// services, which themselves depend on Schedule, without a package cycle.
var (
	StartSchedulePostgresForTest       = startSchedulePostgres
	NewScheduleTestServiceForTest      = newScheduleTestService
	NewScheduleTestBusForTest          = newScheduleTestBus
	CreateDueScheduleForTest           = createDueSchedule
	SeedActiveScheduleRunForTest       = seedActiveScheduleRun
	CompanyPrincipalForTest            = companyPrincipal
	SupersedeQueuedScheduleRunForTest  = supersedeQueuedScheduleRun
	InvalidateQueuedScheduleRunForTest = invalidateQueuedScheduleRun
)

type RecordingMaterializerForTest = recordingMaterializer

func (m *recordingMaterializer) CallsForTest() int { return len(m.snapshot()) }
