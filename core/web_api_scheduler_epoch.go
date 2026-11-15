//go:build webpanel && !android

package core

import "time"

func (tx *WebAPIReadTx) SchedulerEpoch() string {
	return string(tx.tx.Bucket([]byte("meta")).Get([]byte("scheduler-epoch")))
}

func (tx *WebAPIWriteTx) ResetSchedulerAttempts() error {
	epoch, err := webSchedulerID()
	if err != nil {
		return err
	}
	if tx.tx.Bucket([]byte(webSchedulerBucket)) != nil {
		if err = tx.tx.DeleteBucket([]byte(webSchedulerBucket)); err != nil {
			return err
		}
	}
	if _, err = tx.tx.CreateBucket([]byte(webSchedulerBucket)); err != nil {
		return err
	}
	return tx.tx.Bucket([]byte("meta")).Put([]byte("scheduler-epoch"), []byte(epoch))
}

func webSchedulerDuration(millis int64) time.Duration {
	if millis > 9223372036854 {
		millis = 9223372036854
	}
	return time.Duration(millis) * time.Millisecond
}
