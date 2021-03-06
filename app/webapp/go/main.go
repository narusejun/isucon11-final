package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"

	"github.com/go-sql-driver/mysql"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/crypto/bcrypt"
)

const (
	SQLDirectory              = "../sql/"
	AssignmentsDirectory      = "../assignments/"
	InitDataDirectory         = "../data/"
	SessionName               = "isucholar_go"
	mysqlErrNumDuplicateEntry = 1062
)

type handlers struct {
	DB    *sqlx.DB
	SubDB *sqlx.DB
}

var (
	x int64
)

func (h *handlers) Balance() *sqlx.DB {
	v := atomic.AddInt64(&x, 1) % 7
	if v == 0 || v == 2 || v == 4 {
		return h.DB
	} else {
		return h.SubDB
	}
}

// DefaultJSONSerializer implements JSON encoding using encoding/json.
type DefaultJSONSerializer struct{}

// Serialize converts an interface into a json and writes it to the response.
// You can optionally use the indent parameter to produce pretty JSONs.
func (d DefaultJSONSerializer) Serialize(c echo.Context, i interface{}, indent string) error {
	enc := json.NewEncoder(c.Response())
	if indent != "" {
		enc.SetIndent("", indent)
	}
	return enc.Encode(i)
}

// Deserialize reads a JSON from a request body and converts it into an interface.
func (d DefaultJSONSerializer) Deserialize(c echo.Context, i interface{}) error {
	err := json.NewDecoder(c.Request().Body).Decode(i)
	if ute, ok := err.(*json.UnmarshalTypeError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unmarshal type error: expected=%v, got=%v, field=%v, offset=%v", ute.Type, ute.Value, ute.Field, ute.Offset)).SetInternal(err)
	} else if se, ok := err.(*json.SyntaxError); ok {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Syntax error: offset=%v, error=%v", se.Offset, se.Error())).SetInternal(err)
	}
	return err
}

func load(logger echo.Logger) {
	f, err := os.Open("./submission_cache")
	if os.IsNotExist(err) {
		logger.Info("File not exist")
		return
	}

	if err != nil {
		logger.Fatal(err)
	}
	defer f.Close()

	dec := gob.NewDecoder(f)
	if err := dec.Decode(&ClassSubmissionCache); err != nil {
		logger.Fatal("decode error:", err)
	}
	logger.Infof("loaded: %+v", ClassSubmissionCache)
}

func shutdownHook(logger echo.Logger) {
	logger.Info("Output to gob file")
	f, err := os.Create("./submission_cache")
	if err != nil {
		logger.Fatal(err)
	}
	defer f.Close()
	enc := gob.NewEncoder(f)

	if err := enc.Encode(ClassSubmissionCache); err != nil {
		logger.Fatal(err)
	}
}

func main() {
	var err error
	if time.Local, err = time.LoadLocation("UTC"); err != nil {
		panic(err)
	}

	e := echo.New()
	// e.Debug = GetEnv("DEBUG", "") == "true"
	e.HideBanner = true
	e.JSONSerializer = &DefaultJSONSerializer{}

	// e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(session.Middleware(sessions.NewCookieStore([]byte("trapnomura"))))

	load(e.Logger)

	db, _ := GetDB(false)
	db.SetMaxOpenConns(200)

	subdb, _ := GetSubDB(false)
	db.SetMaxOpenConns(200)

	h := &handlers{
		DB:    db,
		SubDB: subdb,
	}

	e.POST("/initialize", h.Initialize)

	e.POST("/login", h.Login)
	e.POST("/logout", h.Logout)
	API := e.Group("/api", h.IsLoggedIn)
	{
		usersAPI := API.Group("/users")
		{
			usersAPI.GET("/me", h.GetMe)
			usersAPI.GET("/me/courses", h.GetRegisteredCourses)
			usersAPI.PUT("/me/courses", h.RegisterCourses)
			usersAPI.GET("/me/grades", h.GetGrades)
		}
		coursesAPI := API.Group("/courses")
		{
			coursesAPI.GET("", h.SearchCourses)
			coursesAPI.POST("", h.AddCourse, h.IsAdmin)
			coursesAPI.GET("/:courseID", h.GetCourseDetail)
			coursesAPI.PUT("/:courseID/status", h.SetCourseStatus, h.IsAdmin)
			coursesAPI.GET("/:courseID/classes", h.GetClasses)
			coursesAPI.POST("/:courseID/classes", h.AddClass, h.IsAdmin)
			coursesAPI.POST("/:courseID/classes/:classID/assignments", h.SubmitAssignment)
			coursesAPI.PUT("/:courseID/classes/:classID/assignments/scores", h.RegisterScores, h.IsAdmin)
			coursesAPI.GET("/:courseID/classes/:classID/assignments/export", h.DownloadSubmittedAssignments, h.IsAdmin)
		}
		announcementsAPI := API.Group("/announcements")
		{
			announcementsAPI.GET("", h.GetAnnouncementList)
			announcementsAPI.POST("", h.AddAnnouncement, h.IsAdmin)
			announcementsAPI.GET("/:announcementID", h.GetAnnouncementDetail)
		}
	}

	if GetEnv("USE_SOCKET", "0") == "1" {
		// ???????????????????????????????????? ---
		socket_file := "/var/run/app.sock"
		os.Remove(socket_file)

		l, err := net.Listen("unix", socket_file)
		if err != nil {
			e.Logger.Fatal(err)
		}

		// go run????????????nginx???????????????????????????????????????????????????777???????????????ok
		err = os.Chmod(socket_file, 0777)
		if err != nil {
			e.Logger.Fatal(err)
		}

		e.Listener = l
		e.Logger.Fatal(e.Start(""))
		// Start server
		go func() {
			if err := e.Start(""); err != nil && err != http.ErrServerClosed {
				e.Logger.Fatal("shutting down the server")
			}
		}()
	} else {
		e.Server.Addr = fmt.Sprintf(":%v", GetEnv("PORT", "7000"))
		// Start server
		go func() {
			if err := e.StartServer(e.Server); err != nil && err != http.ErrServerClosed {
				e.Logger.Fatal("shutting down the server")
			}
		}()
	}

	// Wait for interrupt signal to gracefully shutdown the server with a timeout of 10 seconds.
	// Use a buffered channel to avoid missing signals as recommended for signal.Notify
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownHook(e.Logger)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		e.Logger.Fatal(err)
	}
}

type InitializeResponse struct {
	Language string `json:"language"`
}

// Initialize POST /initialize ??????????????????????????????
func (h *handlers) Initialize(c echo.Context) error {
	dbForInit, _ := GetDB(true)
	SubdbForInit, _ := GetSubDB(true)

	files := []string{
		"1_schema.sql",
		"2_init.sql",
		"3_sample.sql",
	}
	for _, file := range files {
		data, err := os.ReadFile(SQLDirectory + file)
		if err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if _, err := dbForInit.Exec(string(data)); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if _, err := SubdbForInit.Exec(string(data)); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	if err := exec.Command("rm", "-rf", AssignmentsDirectory).Run(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if err := exec.Command("cp", "-r", InitDataDirectory, AssignmentsDirectory).Run(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// totalScore??????????????????
	type totalScoreS struct {
		CourseID   string `db:"course_id"`
		UserID     string `db:"user_id"`
		TotalScore int    `db:"total_score"`
	}
	var totals []totalScoreS
	query := "SELECT `courses`.`id` AS `course_id`, `users`.`id` AS `user_id`, IFNULL(SUM(`submissions`.`score`), 0) AS `total_score`" +
		" FROM `users`" +
		" JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
		" JOIN `courses` ON `registrations`.`course_id` = `courses`.`id`" +
		" LEFT JOIN `classes` ON `courses`.`id` = `classes`.`course_id`" +
		" LEFT JOIN `submissions` ON `users`.`id` = `submissions`.`user_id` AND `submissions`.`class_id` = `classes`.`id`" +
		" GROUP BY `courses`.`id`, `users`.`id`"
	if err := h.Balance().Select(&totals, query); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	for _, total := range totals {
		if _, err := h.DB.Exec("INSERT INTO `user_course_total_scores` (`user_id`, `course_id`, `total_score`) VALUES (?, ?, ?)", total.UserID, total.CourseID, total.TotalScore); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if _, err := h.SubDB.Exec("INSERT INTO `user_course_total_scores` (`user_id`, `course_id`, `total_score`) VALUES (?, ?, ?)", total.UserID, total.CourseID, total.TotalScore); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	res := InitializeResponse{
		Language: "go",
	}

	CourseCacheMux.Lock()
	CourseCacheMap = make(map[string]*Course)
	CourseCacheMux.Unlock()

	ClassCacheMux.Lock()
	ClassCacheMap = make(map[string]*Class)
	ClassCacheMux.Unlock()

	ClassSubmissionMux.Lock()
	ClassSubmissionCache = make(map[string]struct{})
	ClassSubmissionMux.Unlock()

	return c.JSON(http.StatusOK, res)
}

// IsLoggedIn ?????????????????????middleware
func (h *handlers) IsLoggedIn(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		uid, _, _ := getSession(c)
		if uid == "-" {
			return c.String(http.StatusUnauthorized, "You are not logged in.")
		}

		return next(c)
	}
}

// IsAdmin admin?????????middleware
func (h *handlers) IsAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		_, _, isAdmin := getSession(c)
		if !isAdmin {
			return c.String(http.StatusForbidden, "You are not admin user.")
		}

		return next(c)
	}
}

func getUserInfo(c echo.Context) (userID string, userName string, isAdmin bool, err error) {
	userID, userName, isAdmin = getSession(c)
	return userID, userName, isAdmin, nil
}

type UserType string

const (
	Student UserType = "student"
	Teacher UserType = "teacher"
)

type User struct {
	ID             string   `db:"id"`
	Code           string   `db:"code"`
	Name           string   `db:"name"`
	HashedPassword []byte   `db:"hashed_password"`
	Type           UserType `db:"type"`
}

type CourseType string

const (
	LiberalArts   CourseType = "liberal-arts"
	MajorSubjects CourseType = "major-subjects"
)

type DayOfWeek string

const (
	Monday    DayOfWeek = "monday"
	Tuesday   DayOfWeek = "tuesday"
	Wednesday DayOfWeek = "wednesday"
	Thursday  DayOfWeek = "thursday"
	Friday    DayOfWeek = "friday"
)

var daysOfWeek = []DayOfWeek{Monday, Tuesday, Wednesday, Thursday, Friday}

type CourseStatus string

const (
	StatusRegistration CourseStatus = "registration"
	StatusInProgress   CourseStatus = "in-progress"
	StatusClosed       CourseStatus = "closed"
)

type Course struct {
	ID          string       `db:"id"`
	Code        string       `db:"code"`
	Type        CourseType   `db:"type"`
	Name        string       `db:"name"`
	Description string       `db:"description"`
	Credit      uint8        `db:"credit"`
	Period      uint8        `db:"period"`
	DayOfWeek   DayOfWeek    `db:"day_of_week"`
	TeacherID   string       `db:"teacher_id"`
	Keywords    string       `db:"keywords"`
	Status      CourseStatus `db:"status"`
}

// ---------- Public API ----------

type LoginRequest struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

// Login POST /login ????????????
func (h *handlers) Login(c echo.Context) error {
	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	var user User
	if err := h.Balance().Get(&user, "SELECT * FROM `users` WHERE `code` = ?", req.Code); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusUnauthorized, "Code or Password is wrong.")
	}

	if bcrypt.CompareHashAndPassword(user.HashedPassword, []byte(req.Password)) != nil {
		return c.String(http.StatusUnauthorized, "Code or Password is wrong.")
	}

	currentUID, _, _ := getSession(c)
	if currentUID == user.ID {
		return c.String(http.StatusBadRequest, "You are already logged in.")
	}

	setSession(c, user.ID, user.Name, user.Type == Teacher)
	return c.NoContent(http.StatusOK)
}

// Logout POST /logout ???????????????
func (h *handlers) Logout(c echo.Context) error {
	removeSession(c)
	return c.NoContent(http.StatusOK)
}

// ---------- Users API ----------

type GetMeResponse struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
}

// GetMe GET /api/users/me ????????????????????????
func (h *handlers) GetMe(c echo.Context) error {
	userID, userName, isAdmin, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var userCode string
	if err := h.Balance().Get(&userCode, "SELECT `code` FROM `users` WHERE `id` = ?", userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.JSON(http.StatusOK, GetMeResponse{
		Code:    userCode,
		Name:    userName,
		IsAdmin: isAdmin,
	})
}

type GetRegisteredCourseResponseContent struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Teacher   string    `json:"teacher"`
	Period    uint8     `json:"period"`
	DayOfWeek DayOfWeek `json:"day_of_week"`
}

// GetRegisteredCourses GET /api/users/me/courses ??????????????????????????????
func (h *handlers) GetRegisteredCourses(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var courses []Course
	query := "SELECT `courses`.*" +
		" FROM `courses`" +
		" JOIN `registrations` ON `courses`.`id` = `registrations`.`course_id`" +
		" WHERE `courses`.`status` != ? AND `registrations`.`user_id` = ?"
	if err := tx.Select(&courses, query, StatusClosed, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// ???????????????0??????????????????????????????
	res := make([]GetRegisteredCourseResponseContent, 0, len(courses))
	for _, course := range courses {
		var teacher User
		if err := tx.Get(&teacher, "SELECT * FROM `users` WHERE `id` = ?", course.TeacherID); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}

		res = append(res, GetRegisteredCourseResponseContent{
			ID:        course.ID,
			Name:      course.Name,
			Teacher:   teacher.Name,
			Period:    course.Period,
			DayOfWeek: course.DayOfWeek,
		})
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.JSON(http.StatusOK, res)
}

type RegisterCourseRequestContent struct {
	ID string `json:"id"`
}

type RegisterCoursesErrorResponse struct {
	CourseNotFound       []string `json:"course_not_found,omitempty"`
	NotRegistrableStatus []string `json:"not_registrable_status,omitempty"`
	ScheduleConflict     []string `json:"schedule_conflict,omitempty"`
}

// RegisterCourses PUT /api/users/me/courses ????????????
func (h *handlers) RegisterCourses(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var req []RegisterCourseRequestContent
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}
	sort.Slice(req, func(i, j int) bool {
		return req[i].ID < req[j].ID
	})

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var errors RegisterCoursesErrorResponse
	var newlyAdded []Course
	for _, courseReq := range req {
		courseID := courseReq.ID
		ok, course := h.getCourse(courseID)
		if !ok {
			errors.CourseNotFound = append(errors.CourseNotFound, courseReq.ID)
			continue
		}

		if course.Status != StatusRegistration {
			errors.NotRegistrableStatus = append(errors.NotRegistrableStatus, course.ID)
			continue
		}

		// ???????????????????????????????????????????????????
		var count int
		if err := tx.Get(&count, "SELECT COUNT(*) FROM `registrations` WHERE `course_id` = ? AND `user_id` = ? LIMIT 1", course.ID, userID); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if count > 0 {
			continue
		}

		newlyAdded = append(newlyAdded, *course)
	}

	var alreadyRegistered []Course
	query := "SELECT `courses`.*" +
		" FROM `courses`" +
		" JOIN `registrations` ON `courses`.`id` = `registrations`.`course_id`" +
		" WHERE `courses`.`status` != ? AND `registrations`.`user_id` = ?"
	if err := tx.Select(&alreadyRegistered, query, StatusClosed, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	alreadyRegistered = append(alreadyRegistered, newlyAdded...)
	for _, course1 := range newlyAdded {
		for _, course2 := range alreadyRegistered {
			if course1.ID != course2.ID && course1.Period == course2.Period && course1.DayOfWeek == course2.DayOfWeek {
				errors.ScheduleConflict = append(errors.ScheduleConflict, course1.ID)
				break
			}
		}
	}

	if len(errors.CourseNotFound) > 0 || len(errors.NotRegistrableStatus) > 0 || len(errors.ScheduleConflict) > 0 {
		return c.JSON(http.StatusBadRequest, errors)
	}

	for _, course := range newlyAdded {
		h.SubDB.Exec("INSERT INTO `registrations` (`course_id`, `user_id`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `course_id` = VALUES(`course_id`), `user_id` = VALUES(`user_id`)", course.ID, userID)
		_, err = tx.Exec("INSERT INTO `registrations` (`course_id`, `user_id`) VALUES (?, ?) ON DUPLICATE KEY UPDATE `course_id` = VALUES(`course_id`), `user_id` = VALUES(`user_id`)", course.ID, userID)
		if err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	if err = tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}

type Class struct {
	ID               string `db:"id"`
	CourseID         string `db:"course_id"`
	Part             uint8  `db:"part"`
	Title            string `db:"title"`
	Description      string `db:"description"`
	SubmissionClosed bool   `db:"submission_closed"`
}

type GetGradeResponse struct {
	Summary       Summary        `json:"summary"`
	CourseResults []CourseResult `json:"courses"`
}

type Summary struct {
	Credits   int     `json:"credits"`
	GPA       float64 `json:"gpa"`
	GpaTScore float64 `json:"gpa_t_score"` // ?????????
	GpaAvg    float64 `json:"gpa_avg"`     // ?????????
	GpaMax    float64 `json:"gpa_max"`     // ?????????
	GpaMin    float64 `json:"gpa_min"`     // ?????????
}

type CourseResult struct {
	Name             string       `json:"name"`
	Code             string       `json:"code"`
	TotalScore       int          `json:"total_score"`
	TotalScoreTScore float64      `json:"total_score_t_score"` // ?????????
	TotalScoreAvg    float64      `json:"total_score_avg"`     // ?????????
	TotalScoreMax    int          `json:"total_score_max"`     // ?????????
	TotalScoreMin    int          `json:"total_score_min"`     // ?????????
	ClassScores      []ClassScore `json:"class_scores"`
}

type ClassScore struct {
	ClassID    string `json:"class_id"`
	Title      string `json:"title"`
	Part       uint8  `json:"part"`
	Score      *int   `json:"score"`      // 0~100???
	Submitters int    `json:"submitters"` // ?????????????????????
}

// GetGrades GET /api/users/me/grades ????????????
func (h *handlers) GetGrades(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// ????????????????????????????????????
	var registeredCourses []Course
	query := "SELECT `courses`.`id`, `courses`.`name`, `courses`.`code`, `courses`.`credit`, `courses`.`status` FROM `registrations` JOIN `courses` ON `registrations`.`course_id` = `courses`.`id` WHERE `user_id` = ?"
	if err := h.Balance().Select(&registeredCourses, query, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	courseMap := map[string]Course{}
	var sb strings.Builder
	for i, course := range registeredCourses {
		sb.WriteString("'")
		sb.WriteString(course.ID)
		sb.WriteString("'")
		if i < len(registeredCourses)-1 {
			sb.WriteString(",")
		}
		courseMap[course.ID] = course
	}

	// ????????????????????????class??????
	var classes []Class
	query = "SELECT id, course_id, part, title FROM `classes` WHERE `course_id` IN (" + sb.String() + ") ORDER BY `course_id`, `part` DESC"
	if err := h.Balance().Select(&classes, query); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// ????????????????????????????????????TotalScore??????
	type totalS struct {
		CourseId string `db:"course_id"`
		Total    int    `db:"total_score"`
	}
	var totals []totalS
	query = "SELECT course_id, total_score FROM user_course_total_scores WHERE `course_id` IN (" + sb.String() + ")"
	if err := h.Balance().Select(&totals, query); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	totalsMap := map[string][]int{}
	for _, total := range totals {
		totalsMap[total.CourseId] = append(totalsMap[total.CourseId], total.Total)
	}

	// ???????????????????????????????????????????????????
	query = "SELECT `class_id`, `score` FROM `submissions` WHERE `user_id` = ?"
	type scoreS struct {
		ClassId string        `db:"class_id"`
		Score   sql.NullInt64 `db:"score"`
	}
	var myScores []scoreS
	if err := h.Balance().Select(&myScores, query, userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	myScoresMap := make(map[string]sql.NullInt64, len(myScores))
	for _, score := range myScores {
		myScoresMap[score.ClassId] = score.Score
	}

	// ???????????????????????????????????????
	sb.Reset()
	for i, class := range classes {
		sb.WriteString("'")
		sb.WriteString(class.ID)
		sb.WriteString("'")
		if i < len(classes)-1 {
			sb.WriteString(",")
		}
	}
	query = "SELECT class_id, COUNT(*) AS count FROM `submissions` WHERE `class_id` IN (" + sb.String() + ") GROUP BY class_id"
	type submissionS struct {
		ClassId string `db:"class_id"`
		Count   int    `db:"count"`
	}
	var submissions []submissionS
	if err := h.Balance().Select(&submissions, query); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	submissionsMap := make(map[string]int, len(submissions))
	for _, sub := range submissions {
		submissionsMap[sub.ClassId] = sub.Count
	}

	myTotalScores := map[string]int{}
	classScores := make(map[string][]ClassScore, len(classes))
	for _, class := range classes {
		myScore := myScoresMap[class.ID]
		if !myScore.Valid {
			classScores[class.CourseID] = append(classScores[class.CourseID], ClassScore{
				ClassID:    class.ID,
				Part:       class.Part,
				Title:      class.Title,
				Score:      nil,
				Submitters: submissionsMap[class.ID],
			})
		} else {
			score := int(myScore.Int64)
			myTotalScores[class.CourseID] += score
			classScores[class.CourseID] = append(classScores[class.CourseID], ClassScore{
				ClassID:    class.ID,
				Part:       class.Part,
				Title:      class.Title,
				Score:      &score,
				Submitters: submissionsMap[class.ID],
			})
		}
	}

	// ??????????????????????????????
	courseResults := make([]CourseResult, 0, len(registeredCourses))
	myGPA := 0.0
	myCredits := 0
	for _, course := range registeredCourses {
		// ??????????????????????????????????????????TotalScore???????????????
		totals := totalsMap[course.ID]
		courseResults = append(courseResults, CourseResult{
			Name:             course.Name,
			Code:             course.Code,
			TotalScore:       myTotalScores[course.ID],
			TotalScoreTScore: tScoreInt(myTotalScores[course.ID], totals),
			TotalScoreAvg:    averageInt(totals, 0),
			TotalScoreMax:    maxInt(totals, 0),
			TotalScoreMin:    minInt(totals, 0),
			ClassScores:      classScores[course.ID],
		})

		// ?????????GPA??????
		if course.Status == StatusClosed {
			myGPA += float64(myTotalScores[course.ID] * int(course.Credit))
			myCredits += int(course.Credit)
		}
	}
	if myCredits > 0 {
		myGPA = myGPA / 100 / float64(myCredits)
	}

	// GPA????????????
	// ????????????????????????????????????????????????GPA??????
	gpas, err := h.getGPAStats()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	res := GetGradeResponse{
		Summary: Summary{
			Credits:   myCredits,
			GPA:       myGPA,
			GpaTScore: tScoreFloat64(myGPA, gpas),
			GpaAvg:    averageFloat64(gpas, 0),
			GpaMax:    maxFloat64(gpas, 0),
			GpaMin:    minFloat64(gpas, 0),
		},
		CourseResults: courseResults,
	}

	return c.JSON(http.StatusOK, res)
}

// ---------- Courses API ----------

// SearchCourses GET /api/courses ????????????
func (h *handlers) SearchCourses(c echo.Context) error {
	query := "SELECT `courses`.*, `users`.`name` AS `teacher`" +
		" FROM `courses` JOIN `users` ON `courses`.`teacher_id` = `users`.`id`" +
		" WHERE 1=1"
	var condition string
	var args []interface{}

	// ???????????????????????????????????????????????????????????????

	if courseType := c.QueryParam("type"); courseType != "" {
		condition += " AND `courses`.`type` = ?"
		args = append(args, courseType)
	}

	if credit, err := strconv.Atoi(c.QueryParam("credit")); err == nil && credit > 0 {
		condition += " AND `courses`.`credit` = ?"
		args = append(args, credit)
	}

	if teacher := c.QueryParam("teacher"); teacher != "" {
		condition += " AND `users`.`name` = ?"
		args = append(args, teacher)
	}

	if period, err := strconv.Atoi(c.QueryParam("period")); err == nil && period > 0 {
		condition += " AND `courses`.`period` = ?"
		args = append(args, period)
	}

	if dayOfWeek := c.QueryParam("day_of_week"); dayOfWeek != "" {
		condition += " AND `courses`.`day_of_week` = ?"
		args = append(args, dayOfWeek)
	}

	if keywords := c.QueryParam("keywords"); keywords != "" {
		arr := strings.Split(keywords, " ")
		var nameCondition string
		for _, keyword := range arr {
			nameCondition += " AND `courses`.`name` LIKE ?"
			args = append(args, "%"+keyword+"%")
		}
		var keywordsCondition string
		for _, keyword := range arr {
			keywordsCondition += " AND `courses`.`keywords` LIKE ?"
			args = append(args, "%"+keyword+"%")
		}
		condition += fmt.Sprintf(" AND ((1=1%s) OR (1=1%s))", nameCondition, keywordsCondition)
	}

	if status := c.QueryParam("status"); status != "" {
		condition += " AND `courses`.`status` = ?"
		args = append(args, status)
	}

	condition += " ORDER BY `courses`.`code`"

	var page int
	if c.QueryParam("page") == "" {
		page = 1
	} else {
		var err error
		page, err = strconv.Atoi(c.QueryParam("page"))
		if err != nil || page <= 0 {
			return c.String(http.StatusBadRequest, "Invalid page.")
		}
	}
	limit := 20
	offset := limit * (page - 1)

	// limit??????????????????????????????????????????limit?????????????????????????????????????????????????????????????????????????????????
	condition += " LIMIT ? OFFSET ?"
	args = append(args, limit+1, offset)

	// ?????????0??????????????????????????????
	res := make([]GetCourseDetailResponse, 0)
	if err := h.Balance().Select(&res, query+condition, args...); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var links []string
	linkURL, err := url.Parse(c.Request().URL.Path + "?" + c.Request().URL.RawQuery)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	q := linkURL.Query()
	if page > 1 {
		q.Set("page", strconv.Itoa(page-1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"prev\"", linkURL))
	}
	if len(res) > limit {
		q.Set("page", strconv.Itoa(page+1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"next\"", linkURL))
	}
	if len(links) > 0 {
		c.Response().Header().Set("Link", strings.Join(links, ","))
	}

	if len(res) == limit+1 {
		res = res[:len(res)-1]
	}

	return c.JSON(http.StatusOK, res)
}

type AddCourseRequest struct {
	Code        string     `json:"code"`
	Type        CourseType `json:"type"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Credit      int        `json:"credit"`
	Period      int        `json:"period"`
	DayOfWeek   DayOfWeek  `json:"day_of_week"`
	Keywords    string     `json:"keywords"`
}

type AddCourseResponse struct {
	ID string `json:"id"`
}

var (
	CourseCacheMap = make(map[string]*Course)
	CourseCacheMux = sync.RWMutex{}
)

func (h *handlers) getCourse(courseID string) (bool, *Course) {
	CourseCacheMux.RLock()

	if course, ok := CourseCacheMap[courseID]; ok {
		CourseCacheMux.RUnlock()
		return true, course
	}
	CourseCacheMux.RUnlock()

	course := &Course{}
	if err := h.Balance().Get(course, "SELECT * FROM `courses` WHERE `id` = ?", courseID); err != nil {
		return false, nil
	}
	CourseCacheMux.Lock()
	CourseCacheMap[courseID] = course
	CourseCacheMux.Unlock()
	return true, course
}

// AddCourse POST /api/courses ??????????????????
func (h *handlers) AddCourse(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var req AddCourseRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	if req.Type != LiberalArts && req.Type != MajorSubjects {
		return c.String(http.StatusBadRequest, "Invalid course type.")
	}
	if !contains(daysOfWeek, req.DayOfWeek) {
		return c.String(http.StatusBadRequest, "Invalid day of week.")
	}

	courseID := newULID()
	course := &Course{
		ID:          courseID,
		Code:        req.Code,
		Type:        req.Type,
		Name:        req.Name,
		Description: req.Description,
		Credit:      uint8(req.Credit),
		Period:      uint8(req.Period),
		DayOfWeek:   req.DayOfWeek,
		TeacherID:   userID,
		Keywords:    req.Keywords,
		Status:      StatusRegistration,
	}

	_, err = h.SubDB.Exec("INSERT INTO `courses` (`id`, `code`, `type`, `name`, `description`, `credit`, `period`, `day_of_week`, `teacher_id`, `keywords`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		courseID, req.Code, req.Type, req.Name, req.Description, req.Credit, req.Period, req.DayOfWeek, userID, req.Keywords)
	_, err = h.DB.Exec("INSERT INTO `courses` (`id`, `code`, `type`, `name`, `description`, `credit`, `period`, `day_of_week`, `teacher_id`, `keywords`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		courseID, req.Code, req.Type, req.Name, req.Description, req.Credit, req.Period, req.DayOfWeek, userID, req.Keywords)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == uint16(mysqlErrNumDuplicateEntry) {
			var course Course
			if err := h.Balance().Get(&course, "SELECT * FROM `courses` WHERE `code` = ?", req.Code); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			if req.Type != course.Type || req.Name != course.Name || req.Description != course.Description || req.Credit != int(course.Credit) || req.Period != int(course.Period) || req.DayOfWeek != course.DayOfWeek || req.Keywords != course.Keywords {
				return c.String(http.StatusConflict, "A course with the same code already exists.")
			}
			return c.JSON(http.StatusCreated, AddCourseResponse{ID: course.ID})
		}
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	CourseCacheMux.Lock()
	CourseCacheMap[courseID] = course
	CourseCacheMux.Unlock()

	return c.JSON(http.StatusCreated, AddCourseResponse{ID: courseID})
}

type GetCourseDetailResponse struct {
	ID          string       `json:"id" db:"id"`
	Code        string       `json:"code" db:"code"`
	Type        string       `json:"type" db:"type"`
	Name        string       `json:"name" db:"name"`
	Description string       `json:"description" db:"description"`
	Credit      uint8        `json:"credit" db:"credit"`
	Period      uint8        `json:"period" db:"period"`
	DayOfWeek   string       `json:"day_of_week" db:"day_of_week"`
	TeacherID   string       `json:"-" db:"teacher_id"`
	Keywords    string       `json:"keywords" db:"keywords"`
	Status      CourseStatus `json:"status" db:"status"`
	Teacher     string       `json:"teacher" db:"teacher"`
}

// GetCourseDetail GET /api/courses/:courseID ?????????????????????
func (h *handlers) GetCourseDetail(c echo.Context) error {
	courseID := c.Param("courseID")

	var res GetCourseDetailResponse
	query := "SELECT `courses`.*, `users`.`name` AS `teacher`" +
		" FROM `courses`" +
		" JOIN `users` ON `courses`.`teacher_id` = `users`.`id`" +
		" WHERE `courses`.`id` = ?"
	if err := h.Balance().Get(&res, query, courseID); err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such course.")
	}

	return c.JSON(http.StatusOK, res)
}

type SetCourseStatusRequest struct {
	Status CourseStatus `json:"status"`
}

// SetCourseStatus PUT /api/courses/:courseID/status ?????????????????????????????????
func (h *handlers) SetCourseStatus(c echo.Context) error {
	courseID := c.Param("courseID")

	var req SetCourseStatusRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	ok, course := h.getCourse(courseID)
	if !ok {
		return c.String(http.StatusNotFound, "No such course.")
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	if _, err := h.SubDB.Exec("UPDATE `courses` SET `status` = ? WHERE `id` = ?", req.Status, courseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if _, err := tx.Exec("UPDATE `courses` SET `status` = ? WHERE `id` = ?", req.Status, courseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	course.Status = req.Status

	return c.NoContent(http.StatusOK)
}

type ClassWithSubmitted struct {
	ID               string `db:"id"`
	CourseID         string `db:"course_id"`
	Part             uint8  `db:"part"`
	Title            string `db:"title"`
	Description      string `db:"description"`
	SubmissionClosed bool   `db:"submission_closed"`
	Submitted        bool   `db:"submitted"`
}

type GetClassResponse struct {
	ID               string `json:"id"`
	Part             uint8  `json:"part"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	SubmissionClosed bool   `json:"submission_closed"`
	Submitted        bool   `json:"submitted"`
}

var (
	ClassSubmissionCache = make(map[string]struct{})
	ClassSubmissionMux   = sync.RWMutex{}
)

func (h *handlers) isSubmit(classID string, userID string) bool {
	ClassSubmissionMux.RLock()
	_, ok := ClassSubmissionCache[classID+"/"+userID]
	ClassSubmissionMux.RUnlock()
	return ok
}

func (h *handlers) submit(classID string, userID string) {
	ClassSubmissionMux.Lock()
	ClassSubmissionCache[classID+"/"+userID] = struct{}{}
	ClassSubmissionMux.Unlock()
}

// GetClasses GET /api/courses/:courseID/classes ???????????????????????????????????????
func (h *handlers) GetClasses(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	courseID := c.Param("courseID")

	etag := h.getClassesEtag(courseID, userID)
	if etag != "" && c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}

	ok, _ := h.getCourse(courseID)
	if !ok {
		return c.String(http.StatusNotFound, "No such course.")
	}

	var classes []ClassWithSubmitted
	query := "SELECT `classes`.*" +
		" FROM `classes`" +
		" WHERE `classes`.`course_id` = ?" +
		" ORDER BY `classes`.`part`"
	if err := h.Balance().Select(&classes, query, courseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	// ?????????0??????????????????????????????
	res := make([]GetClassResponse, 0, len(classes))
	for _, class := range classes {
		res = append(res, GetClassResponse{
			ID:               class.ID,
			Part:             class.Part,
			Title:            class.Title,
			Description:      class.Description,
			SubmissionClosed: class.SubmissionClosed,
			Submitted:        h.isSubmit(class.ID, userID),
		})
	}

	c.Response().Header().Set("ETag", h.setClassesEtag(courseID, userID))
	return c.JSON(http.StatusOK, res)
}

type AddClassRequest struct {
	Part        uint8  `json:"part"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type AddClassResponse struct {
	ClassID string `json:"class_id"`
}

var (
	ClassCacheMap = make(map[string]*Class)
	ClassCacheMux = sync.RWMutex{}
)

func (h *handlers) getClass(classID string) (bool, *Class) {
	ClassCacheMux.RLock()

	if class, ok := ClassCacheMap[classID]; ok {
		ClassCacheMux.RUnlock()
		return true, class
	}
	ClassCacheMux.RUnlock()

	class := &Class{}
	if err := h.Balance().Get(class, "SELECT * FROM `classes` WHERE `id` = ?", classID); err != nil {
		return false, nil
	}
	ClassCacheMux.Lock()
	ClassCacheMap[classID] = class
	ClassCacheMux.Unlock()
	return true, class
}

// AddClass POST /api/courses/:courseID/classes ????????????(&??????)??????
func (h *handlers) AddClass(c echo.Context) error {
	courseID := c.Param("courseID")

	var req AddClassRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	ok, course := h.getCourse(courseID)
	if !ok {
		return c.String(http.StatusNotFound, "No such course.")
	}

	if course.Status != StatusInProgress {
		return c.String(http.StatusBadRequest, "This course is not in-progress.")
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	classID := newULID()
	class := &Class{
		ID:               classID,
		CourseID:         courseID,
		Part:             req.Part,
		Title:            req.Title,
		Description:      req.Description,
		SubmissionClosed: false,
	}

	h.SubDB.Exec("INSERT INTO `classes` (`id`, `course_id`, `part`, `title`, `description`) VALUES (?, ?, ?, ?, ?)",
		classID, courseID, req.Part, req.Title, req.Description)
	if _, err := tx.Exec("INSERT INTO `classes` (`id`, `course_id`, `part`, `title`, `description`) VALUES (?, ?, ?, ?, ?)",
		classID, courseID, req.Part, req.Title, req.Description); err != nil {
		_ = tx.Rollback()
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == uint16(mysqlErrNumDuplicateEntry) {
			var class Class
			if err := h.Balance().Get(&class, "SELECT * FROM `classes` WHERE `course_id` = ? AND `part` = ?", courseID, req.Part); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			if req.Title != class.Title || req.Description != class.Description {
				return c.String(http.StatusConflict, "A class with the same part already exists.")
			}
			return c.JSON(http.StatusCreated, AddClassResponse{ClassID: class.ID})
		}
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	ClassCacheMux.Lock()
	ClassCacheMap[classID] = class
	ClassCacheMux.Unlock()

	h.discardClassesEtagByCource(courseID)
	return c.JSON(http.StatusCreated, AddClassResponse{ClassID: classID})
}

// SubmitAssignment POST /api/courses/:courseID/classes/:classID/assignments ???????????????
func (h *handlers) SubmitAssignment(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	courseID := c.Param("courseID")
	classID := c.Param("classID")

	ok, course := h.getCourse(courseID)
	if !ok {
		return c.String(http.StatusNotFound, "No such course.")
	}

	if course.Status != StatusInProgress {
		return c.String(http.StatusBadRequest, "This course is not in progress.")
	}

	registered, err := h.isUserRegistered(userID, courseID)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if !registered {
		return c.String(http.StatusBadRequest, "You have not taken this course.")
	}

	ok, class := h.getClass(classID)
	if !ok {
		return c.String(http.StatusNotFound, "No such class.")
	}
	if class.SubmissionClosed {
		return c.String(http.StatusBadRequest, "Submission has been closed for this class.")
	}

	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return c.String(http.StatusBadRequest, "Invalid file.")
	}
	defer file.Close()

	if _, err := h.DB.Exec("INSERT INTO `submissions` (`user_id`, `class_id`, `file_name`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `file_name` = VALUES(`file_name`)", userID, classID, header.Filename); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if _, err := h.SubDB.Exec("INSERT INTO `submissions` (`user_id`, `class_id`, `file_name`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE `file_name` = VALUES(`file_name`)", userID, classID, header.Filename); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	h.submit(classID, userID)

	dst := AssignmentsDirectory + classID + "-" + userID + ".pdf"
	fd, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if _, err := io.Copy(fd, file); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusNoContent)
}

type Score struct {
	UserCode string `json:"user_code"`
	Score    int    `json:"score"`
}

// RegisterScores PUT /api/courses/:courseID/classes/:classID/assignments/scores ??????????????????
func (h *handlers) RegisterScores(c echo.Context) error {
	classID := c.Param("classID")

	ok, class := h.getClass(classID)

	if !ok {
		return c.String(http.StatusNotFound, "No such class.")
	}

	if !class.SubmissionClosed {
		return c.String(http.StatusBadRequest, "This assignment is not closed yet.")
	}

	var req []Score
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	// TODO ??????????????????
	args := make([]interface{}, 3*len(req)+1)

	for i := 0; i < len(req); i++ {
		args[i] = req[i].UserCode
		args[len(req)+i] = req[i].Score
		args[2*len(req)+i] = req[i].UserCode
	}
	args[3*len(req)] = classID

	if _, err := h.DB.Exec("UPDATE `submissions` JOIN `users` ON `users`.`id` = `submissions`.`user_id` SET `score` = ELT(FIELD(`users`.`code`"+strings.Repeat(", ?", len(req))+"), ?"+strings.Repeat(", ?", len(req)-1)+") WHERE `users`.`code` IN(?"+strings.Repeat(", ?", len(req)-1)+") AND `class_id` = ?", args...); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if _, err := h.SubDB.Exec("UPDATE `submissions` JOIN `users` ON `users`.`id` = `submissions`.`user_id` SET `score` = ELT(FIELD(`users`.`code`"+strings.Repeat(", ?", len(req))+"), ?"+strings.Repeat(", ?", len(req)-1)+") WHERE `users`.`code` IN(?"+strings.Repeat(", ?", len(req)-1)+") AND `class_id` = ?", args...); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	type totalScoreS struct {
		UserID     string `db:"user_id"`
		TotalScore int    `db:"total_score"`
	}
	var totals []totalScoreS
	query := "SELECT `users`.`id` AS `user_id`, IFNULL(SUM(`submissions`.`score`), 0) AS `total_score`" +
		" FROM `users`" +
		" JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
		" JOIN `courses` ON `registrations`.`course_id` = `courses`.`id`" +
		" LEFT JOIN `classes` ON `courses`.`id` = `classes`.`course_id`" +
		" LEFT JOIN `submissions` ON `users`.`id` = `submissions`.`user_id` AND `submissions`.`class_id` = `classes`.`id`" +
		" WHERE `courses`.`id` = ?" +
		" GROUP BY `courses`.`id`, `users`.`id`"
	if err := h.Balance().Select(&totals, query, class.CourseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	for _, total := range totals {
		if _, err := h.DB.Exec("INSERT INTO `user_course_total_scores` (`total_score`, `course_id`, `user_id`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE total_score = ?", total.TotalScore, class.CourseID, total.UserID, total.TotalScore); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
		if _, err := h.SubDB.Exec("INSERT INTO `user_course_total_scores` (`total_score`, `course_id`, `user_id`) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE total_score = ?", total.TotalScore, class.CourseID, total.UserID, total.TotalScore); err != nil {
			c.Logger().Error(err)
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	return c.NoContent(http.StatusNoContent)
}

type Submission struct {
	UserID   string `db:"user_id"`
	UserCode string `db:"user_code"`
	FileName string `db:"file_name"`
}

// DownloadSubmittedAssignments GET /api/courses/:courseID/classes/:classID/assignments/export ????????????????????????????????????zip?????????????????????????????????
func (h *handlers) DownloadSubmittedAssignments(c echo.Context) error {
	courseID := c.Param("courseID")
	classID := c.Param("classID")

	ok, class := h.getClass(classID)
	if !ok {
		return c.String(http.StatusNotFound, "No such class.")
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()
	var submissions []Submission
	query := "SELECT `submissions`.`user_id`, `submissions`.`file_name`, `users`.`code` AS `user_code`" +
		" FROM `submissions`" +
		" JOIN `users` ON `users`.`id` = `submissions`.`user_id`" +
		" WHERE `class_id` = ?"
	if err := tx.Select(&submissions, query, classID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	zipFilePath := AssignmentsDirectory + classID + ".zip"
	if err := createSubmissionsZip(zipFilePath, classID, submissions); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if _, err := h.SubDB.Exec("UPDATE `classes` SET `submission_closed` = true WHERE `id` = ?", classID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if _, err := tx.Exec("UPDATE `classes` SET `submission_closed` = true WHERE `id` = ?", classID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	class.SubmissionClosed = true

	h.discardClassesEtagByCource(courseID)
	return c.File(zipFilePath)
}

func createSubmissionsZip(zipFilePath string, classID string, submissions []Submission) error {
	//tmpDir := AssignmentsDirectory + classID + "/"
	//if err := exec.Command("rm", "-rf", tmpDir).Run(); err != nil {
	//	return err
	//}
	//if err := exec.Command("mkdir", tmpDir).Run(); err != nil {
	//		return err
	//}

	body, err := os.Create(zipFilePath)
	if err != nil {
		return err
	}
	bodyWriter := zip.NewWriter(body)
	defer bodyWriter.Close()
	// ??????????????????????????????????????????

	// eg := errgroup.Group{}
	for _, _submission := range submissions {
		submission := _submission
		filename := AssignmentsDirectory + classID + "-" + submission.UserID + ".pdf"
		f, err := os.Open(filename)
		if err != nil {
			f.Close()
			return err
		}

		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}

		fileInfoHeader, err := zip.FileInfoHeader(fi)
		if err != nil {
			f.Close()
			return err
		}

		fileInfoHeader.Name = submission.UserCode + "-" + submission.FileName

		addedFile, err := bodyWriter.CreateHeader(fileInfoHeader)
		if err != nil {
			f.Close()
			return err
		}

		if _, err := io.Copy(addedFile, f); err != nil {
			f.Close()
			return err
		}
		f.Close()

		//eg.Go(
		//	func() error {
		//	},
		//)
	}
	//if err := eg.Wait(); err != nil {
	//	return err
	//}

	// -i 'tmpDir/*': ???zip?????????
	//return exec.Command("zip", "-j", "-r", zipFilePath, tmpDir, "-i", tmpDir+"*").Run()
	return nil
}

// ---------- Announcement API ----------

type AnnouncementWithoutDetail struct {
	ID         string `json:"id" db:"id"`
	CourseID   string `json:"course_id" db:"course_id"`
	CourseName string `json:"course_name" db:"course_name"`
	Title      string `json:"title" db:"title"`
	Unread     bool   `json:"unread" db:"unread"`
}

type GetAnnouncementsResponse struct {
	UnreadCount   int                         `json:"unread_count"`
	Announcements []AnnouncementWithoutDetail `json:"announcements"`
}

// GetAnnouncementList GET /api/announcements ????????????????????????
func (h *handlers) GetAnnouncementList(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var announcements []AnnouncementWithoutDetail
	var args []interface{}
	query := "SELECT `announcements`.`id`, `courses`.`id` AS `course_id`, `courses`.`name` AS `course_name`, `announcements`.`title`, NOT `unread_announcements`.`is_deleted` AS `unread`" +
		" FROM `announcements`" +
		" JOIN `courses` ON `announcements`.`course_id` = `courses`.`id`" +
		" JOIN `registrations` ON `courses`.`id` = `registrations`.`course_id`" +
		" JOIN `unread_announcements` ON `announcements`.`id` = `unread_announcements`.`announcement_id`" +
		" WHERE 1=1"

	if courseID := c.QueryParam("course_id"); courseID != "" {
		query += " AND `announcements`.`course_id` = ?"
		args = append(args, courseID)
	}

	query += " AND `unread_announcements`.`user_id` = ?" +
		" AND `registrations`.`user_id` = ?" +
		" ORDER BY `announcements`.`id` DESC" +
		" LIMIT ? OFFSET ?"
	args = append(args, userID, userID)

	var page int
	if c.QueryParam("page") == "" {
		page = 1
	} else {
		page, err = strconv.Atoi(c.QueryParam("page"))
		if err != nil || page <= 0 {
			return c.String(http.StatusBadRequest, "Invalid page.")
		}
	}
	limit := 20
	offset := limit * (page - 1)
	// limit??????????????????????????????????????????limit?????????????????????????????????????????????????????????????????????????????????
	args = append(args, limit+1, offset)

	if err := h.Balance().Select(&announcements, query, args...); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var unreadCount int
	if err := h.Balance().Get(&unreadCount, "SELECT COUNT(*) FROM `unread_announcements` WHERE `user_id` = ? AND NOT `is_deleted`", userID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var links []string
	linkURL, err := url.Parse(c.Request().URL.Path + "?" + c.Request().URL.RawQuery)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	q := linkURL.Query()
	if page > 1 {
		q.Set("page", strconv.Itoa(page-1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"prev\"", linkURL))
	}
	if len(announcements) > limit {
		q.Set("page", strconv.Itoa(page+1))
		linkURL.RawQuery = q.Encode()
		links = append(links, fmt.Sprintf("<%v>; rel=\"next\"", linkURL))
	}
	if len(links) > 0 {
		c.Response().Header().Set("Link", strings.Join(links, ","))
	}

	if len(announcements) == limit+1 {
		announcements = announcements[:len(announcements)-1]
	}

	// ???????????????????????????????????????0??????????????????????????????
	announcementsRes := append(make([]AnnouncementWithoutDetail, 0, len(announcements)), announcements...)

	return c.JSON(http.StatusOK, GetAnnouncementsResponse{
		UnreadCount:   unreadCount,
		Announcements: announcementsRes,
	})
}

type Announcement struct {
	ID       string `db:"id"`
	CourseID string `db:"course_id"`
	Title    string `db:"title"`
	Message  string `db:"message"`
}

type AddAnnouncementRequest struct {
	ID       string `json:"id"`
	CourseID string `json:"course_id"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

// AddAnnouncement POST /api/announcements ????????????????????????
func (h *handlers) AddAnnouncement(c echo.Context) error {
	var req AddAnnouncementRequest
	if err := c.Bind(&req); err != nil {
		return c.String(http.StatusBadRequest, "Invalid format.")
	}

	tx, err := h.DB.Beginx()
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer tx.Rollback()

	var count int
	if err := tx.Get(&count, "SELECT COUNT(*) FROM `courses` WHERE `id` = ? LIMIT 1", req.CourseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if count == 0 {
		return c.String(http.StatusNotFound, "No such course.")
	}

	h.SubDB.Exec("INSERT INTO `announcements` (`id`, `course_id`, `title`, `message`) VALUES (?, ?, ?, ?)", req.ID, req.CourseID, req.Title, req.Message)
	if _, err := tx.Exec("INSERT INTO `announcements` (`id`, `course_id`, `title`, `message`) VALUES (?, ?, ?, ?)",
		req.ID, req.CourseID, req.Title, req.Message); err != nil {
		_ = tx.Rollback()
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == uint16(mysqlErrNumDuplicateEntry) {
			var announcement Announcement
			if err := h.Balance().Get(&announcement, "SELECT * FROM `announcements` WHERE `id` = ?", req.ID); err != nil {
				c.Logger().Error(err)
				return c.NoContent(http.StatusInternalServerError)
			}
			if announcement.CourseID != req.CourseID || announcement.Title != req.Title || announcement.Message != req.Message {
				return c.String(http.StatusConflict, "An announcement with the same id already exists.")
			}
			return c.NoContent(http.StatusCreated)
		}
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	var targets []User
	query := "SELECT `users`.* FROM `users`" +
		" JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
		" WHERE `registrations`.`course_id` = ?"
	if err := tx.Select(&targets, query, req.CourseID); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	if len(targets) > 0 {
		var sb strings.Builder
		for i, target := range targets {
			sb.WriteString("('")
			sb.WriteString(req.ID)
			sb.WriteString("','")
			sb.WriteString(target.ID)
			sb.WriteString("')")
			if i < len(targets)-1 {
				sb.WriteString(",")
			}
		}
		h.SubDB.Exec("INSERT INTO `unread_announcements` (`announcement_id`, `user_id`) VALUES " + sb.String())
		if _, err := tx.Exec("INSERT INTO `unread_announcements` (`announcement_id`, `user_id`) VALUES " + sb.String()); err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}
	}

	if err := tx.Commit(); err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusCreated)
}

type AnnouncementDetail struct {
	ID         string `json:"id" db:"id"`
	CourseID   string `json:"course_id" db:"course_id"`
	CourseName string `json:"course_name" db:"course_name"`
	Title      string `json:"title" db:"title"`
	Message    string `json:"message" db:"message"`
	Unread     bool   `json:"unread" db:"unread"`
}

// GetAnnouncementDetail GET /api/announcements/:announcementID ????????????????????????
func (h *handlers) GetAnnouncementDetail(c echo.Context) error {
	userID, _, _, err := getUserInfo(c)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	announcementID := c.Param("announcementID")

	announcementDetail, err := h.getAnnouncementDetail(announcementID)
	if err != nil && err != sql.ErrNoRows {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	} else if err == sql.ErrNoRows {
		return c.String(http.StatusNotFound, "No such announcement.")
	}

	ok, err := h.isUserRegistered(userID, announcementDetail.CourseID)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	if !ok {
		return c.String(http.StatusNotFound, "No such announcement.")
	}

	r, err := h.DB.Exec("UPDATE `unread_announcements` SET `is_deleted` = true WHERE `announcement_id` = ? AND `user_id` = ? AND `is_deleted` = false", announcementID, userID)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}
	r, err = h.SubDB.Exec("UPDATE `unread_announcements` SET `is_deleted` = true WHERE `announcement_id` = ? AND `user_id` = ? AND `is_deleted` = false", announcementID, userID)
	if err != nil {
		c.Logger().Error(err)
		return c.NoContent(http.StatusInternalServerError)
	}

	count, _ := r.RowsAffected()
	announcementDetail.Unread = count > 0

	if !announcementDetail.Unread {
		c.Response().Header().Set("Cache-Control", "max-age=86400")
	}

	return c.JSON(http.StatusOK, announcementDetail)
}
