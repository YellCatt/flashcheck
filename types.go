package main

import "time"

const (
	version       = "1.0.0"
	testFileName  = ".sd_test_tmp"
	blockSize     = 4 * 1024 * 1024
	samplePercent = 100
)

type DeviceInfo struct {
	Index      int
	Name       string
	Path       string
	Removable  bool
	Size       uint64
	Filesystem string
}

type TestResult struct {
	Device       string
	Capacity     uint64
	SeqWrite     float64
	SeqRead      float64
	RandWrite    float64
	RandRead     float64
	Integrity    bool
	BadBlocks    int
	TotalBlocks  int
	TestDuration time.Duration
}

type RawTestResult struct {
	Device          string
	RawPath         string
	SectorsTested   int
	BadReadSectors  int
	BadWriteSectors int
	SectorSize      int
	TestDuration    time.Duration
	PatternDesc     string
}