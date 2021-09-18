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
		query := "SELECT total_score FROM user_course_total_scores WHERE course_id = ?"
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
