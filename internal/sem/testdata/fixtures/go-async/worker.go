package worker

type Job struct {
	ID string
}

func Run(job Job) Job {
	go Publish(job)
	return Build(job)
}

func Publish(job Job) {}

func Build(job Job) Job {
	return job
}
