package main

import (
	"golang.org/x/sync/singleflight"
)

var (
	getTotalScoresS = singleflight.Group{}
)

func (h *handlers) getTotalScores(courseID string) ([]int, error) {
	r, err, _ := getTotalScoresS.Do(courseID, func() (interface{}, error) {
		// この科目を履修している学生のTotalScore一覧を取得
		var totals []int
		query := "SELECT IFNULL(SUM(`submissions`.`score`), 0) AS `total_score`" +
			" FROM `users`" +
			" JOIN `registrations` ON `users`.`id` = `registrations`.`user_id`" +
			" JOIN `courses` ON `registrations`.`course_id` = `courses`.`id`" +
			" LEFT JOIN `classes` ON `courses`.`id` = `classes`.`course_id`" +
			" LEFT JOIN `submissions` ON `users`.`id` = `submissions`.`user_id` AND `submissions`.`class_id` = `classes`.`id`" +
			" WHERE `courses`.`id` = ? GROUP BY `users`.`id`"
		if err := h.DB.Select(&totals, query, courseID); err != nil {
			return nil, err
		}

		return totals, nil
	})
	if err != nil {
		return nil, err
	}
	return r.([]int), nil
}
