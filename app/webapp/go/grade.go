package main

import (
	"golang.org/x/sync/singleflight"
)

var (
	getTotalScoresS = singleflight.Group{}
	getGPAStatsS    = singleflight.Group{}
)

func (h *handlers) getTotalScores(courseID string) ([]int, error) {
	r, err, _ := getTotalScoresS.Do(courseID, func() (interface{}, error) {
		// この科目を履修している学生のTotalScore一覧を取得
		var totals []int
		query := "SELECT total_score FROM user_course_total_scores WHERE course_id = ?"
		if err := h.Balance().Select(&totals, query, courseID); err != nil {
			return nil, err
		}

		return totals, nil
	})
	if err != nil {
		return nil, err
	}
	return r.([]int), nil
}

func (h *handlers) getGPAStats() ([]float64, error) {
	r, err, _ := getGPAStatsS.Do("", func() (interface{}, error) {
		var gpas []float64
		query := "SELECT IFNULL(SUM(`user_course_total_scores`.`total_score` * `courses`.`credit`), 0) / 100 / `credits`.`credits` AS `gpa`" +
			" FROM `users`" +
			" JOIN (" +
			"     SELECT `users`.`id` AS `user_id`, SUM(`courses`.`credit`) AS `credits`" +
			"     FROM `users`" +
			"     JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
			"     JOIN `courses` ON `registrations`.`course_id` = `courses`.`id` AND `courses`.`status` = ?" +
			"     GROUP BY `users`.`id`" +
			" ) AS `credits` ON `credits`.`user_id` = `users`.`id`" +
			" JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
			" JOIN `courses` ON `registrations`.`course_id` = `courses`.`id` AND `courses`.`status` = ?" +
			" LEFT JOIN `user_course_total_scores` ON `users`.`id` = `user_course_total_scores`.`user_id` AND `user_course_total_scores`.`course_id` = `courses`.`id`" +
			" WHERE `users`.`type` = ?" +
			" GROUP BY `users`.`id`"
		if err := h.Balance().Select(&gpas, query, StatusClosed, StatusClosed, Student); err != nil {
			return nil, err
		}
		return gpas, nil
	})
	if err != nil {
		return nil, err
	}
	return r.([]float64), nil
}
