// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.28.1
// 	protoc        v3.21.10
// source: job_restore.proto

package entity

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type JobRestoreStep int32

const (
	JobRestoreStep_PENDING       JobRestoreStep = 0
	JobRestoreStep_WAIT_FOR_TAPE JobRestoreStep = 1
	JobRestoreStep_COPYING       JobRestoreStep = 2
	JobRestoreStep_FINISHED      JobRestoreStep = 255
)

// Enum value maps for JobRestoreStep.
var (
	JobRestoreStep_name = map[int32]string{
		0:   "PENDING",
		1:   "WAIT_FOR_TAPE",
		2:   "COPYING",
		255: "FINISHED",
	}
	JobRestoreStep_value = map[string]int32{
		"PENDING":       0,
		"WAIT_FOR_TAPE": 1,
		"COPYING":       2,
		"FINISHED":      255,
	}
)

func (x JobRestoreStep) Enum() *JobRestoreStep {
	p := new(JobRestoreStep)
	*p = x
	return p
}

func (x JobRestoreStep) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (JobRestoreStep) Descriptor() protoreflect.EnumDescriptor {
	return file_job_restore_proto_enumTypes[0].Descriptor()
}

func (JobRestoreStep) Type() protoreflect.EnumType {
	return &file_job_restore_proto_enumTypes[0]
}

func (x JobRestoreStep) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use JobRestoreStep.Descriptor instead.
func (JobRestoreStep) EnumDescriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{0}
}

type JobRestoreParam struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	FileIds []int64 `protobuf:"varint,1,rep,packed,name=file_ids,json=fileIds,proto3" json:"file_ids,omitempty"`
}

func (x *JobRestoreParam) Reset() {
	*x = JobRestoreParam{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JobRestoreParam) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JobRestoreParam) ProtoMessage() {}

func (x *JobRestoreParam) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JobRestoreParam.ProtoReflect.Descriptor instead.
func (*JobRestoreParam) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{0}
}

func (x *JobRestoreParam) GetFileIds() []int64 {
	if x != nil {
		return x.FileIds
	}
	return nil
}

type JobRestoreDispatchParam struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Param:
	//	*JobRestoreDispatchParam_WaitForTape
	//	*JobRestoreDispatchParam_Copying
	//	*JobRestoreDispatchParam_Finished
	Param isJobRestoreDispatchParam_Param `protobuf_oneof:"param"`
}

func (x *JobRestoreDispatchParam) Reset() {
	*x = JobRestoreDispatchParam{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JobRestoreDispatchParam) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JobRestoreDispatchParam) ProtoMessage() {}

func (x *JobRestoreDispatchParam) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JobRestoreDispatchParam.ProtoReflect.Descriptor instead.
func (*JobRestoreDispatchParam) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{1}
}

func (m *JobRestoreDispatchParam) GetParam() isJobRestoreDispatchParam_Param {
	if m != nil {
		return m.Param
	}
	return nil
}

func (x *JobRestoreDispatchParam) GetWaitForTape() *JobRestoreWaitForTapeParam {
	if x, ok := x.GetParam().(*JobRestoreDispatchParam_WaitForTape); ok {
		return x.WaitForTape
	}
	return nil
}

func (x *JobRestoreDispatchParam) GetCopying() *JobRestoreCopyingParam {
	if x, ok := x.GetParam().(*JobRestoreDispatchParam_Copying); ok {
		return x.Copying
	}
	return nil
}

func (x *JobRestoreDispatchParam) GetFinished() *JobRestoreFinishedParam {
	if x, ok := x.GetParam().(*JobRestoreDispatchParam_Finished); ok {
		return x.Finished
	}
	return nil
}

type isJobRestoreDispatchParam_Param interface {
	isJobRestoreDispatchParam_Param()
}

type JobRestoreDispatchParam_WaitForTape struct {
	WaitForTape *JobRestoreWaitForTapeParam `protobuf:"bytes,1,opt,name=wait_for_tape,json=waitForTape,proto3,oneof"`
}

type JobRestoreDispatchParam_Copying struct {
	Copying *JobRestoreCopyingParam `protobuf:"bytes,2,opt,name=copying,proto3,oneof"`
}

type JobRestoreDispatchParam_Finished struct {
	Finished *JobRestoreFinishedParam `protobuf:"bytes,255,opt,name=finished,proto3,oneof"`
}

func (*JobRestoreDispatchParam_WaitForTape) isJobRestoreDispatchParam_Param() {}

func (*JobRestoreDispatchParam_Copying) isJobRestoreDispatchParam_Param() {}

func (*JobRestoreDispatchParam_Finished) isJobRestoreDispatchParam_Param() {}

type JobRestoreWaitForTapeParam struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *JobRestoreWaitForTapeParam) Reset() {
	*x = JobRestoreWaitForTapeParam{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JobRestoreWaitForTapeParam) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JobRestoreWaitForTapeParam) ProtoMessage() {}

func (x *JobRestoreWaitForTapeParam) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JobRestoreWaitForTapeParam.ProtoReflect.Descriptor instead.
func (*JobRestoreWaitForTapeParam) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{2}
}

type JobRestoreCopyingParam struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Device string `protobuf:"bytes,1,opt,name=device,proto3" json:"device,omitempty"`
}

func (x *JobRestoreCopyingParam) Reset() {
	*x = JobRestoreCopyingParam{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JobRestoreCopyingParam) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JobRestoreCopyingParam) ProtoMessage() {}

func (x *JobRestoreCopyingParam) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JobRestoreCopyingParam.ProtoReflect.Descriptor instead.
func (*JobRestoreCopyingParam) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{3}
}

func (x *JobRestoreCopyingParam) GetDevice() string {
	if x != nil {
		return x.Device
	}
	return ""
}

type JobRestoreFinishedParam struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *JobRestoreFinishedParam) Reset() {
	*x = JobRestoreFinishedParam{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JobRestoreFinishedParam) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JobRestoreFinishedParam) ProtoMessage() {}

func (x *JobRestoreFinishedParam) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JobRestoreFinishedParam.ProtoReflect.Descriptor instead.
func (*JobRestoreFinishedParam) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{4}
}

type RestoreFile struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	FileId     int64      `protobuf:"varint,1,opt,name=file_id,json=fileId,proto3" json:"file_id,omitempty"`
	TapeId     int64      `protobuf:"varint,2,opt,name=tape_id,json=tapeId,proto3" json:"tape_id,omitempty"`
	PositionId int64      `protobuf:"varint,3,opt,name=position_id,json=positionId,proto3" json:"position_id,omitempty"`
	Status     CopyStatus `protobuf:"varint,17,opt,name=status,proto3,enum=copy_status.CopyStatus" json:"status,omitempty"`
	Size       int64      `protobuf:"varint,18,opt,name=size,proto3" json:"size,omitempty"`
	Hash       []byte     `protobuf:"bytes,19,opt,name=hash,proto3" json:"hash,omitempty"`
	TapePath   string     `protobuf:"bytes,33,opt,name=tape_path,json=tapePath,proto3" json:"tape_path,omitempty"`
	TargetPath string     `protobuf:"bytes,34,opt,name=target_path,json=targetPath,proto3" json:"target_path,omitempty"`
}

func (x *RestoreFile) Reset() {
	*x = RestoreFile{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *RestoreFile) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RestoreFile) ProtoMessage() {}

func (x *RestoreFile) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RestoreFile.ProtoReflect.Descriptor instead.
func (*RestoreFile) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{5}
}

func (x *RestoreFile) GetFileId() int64 {
	if x != nil {
		return x.FileId
	}
	return 0
}

func (x *RestoreFile) GetTapeId() int64 {
	if x != nil {
		return x.TapeId
	}
	return 0
}

func (x *RestoreFile) GetPositionId() int64 {
	if x != nil {
		return x.PositionId
	}
	return 0
}

func (x *RestoreFile) GetStatus() CopyStatus {
	if x != nil {
		return x.Status
	}
	return CopyStatus_DRAFT
}

func (x *RestoreFile) GetSize() int64 {
	if x != nil {
		return x.Size
	}
	return 0
}

func (x *RestoreFile) GetHash() []byte {
	if x != nil {
		return x.Hash
	}
	return nil
}

func (x *RestoreFile) GetTapePath() string {
	if x != nil {
		return x.TapePath
	}
	return ""
}

func (x *RestoreFile) GetTargetPath() string {
	if x != nil {
		return x.TargetPath
	}
	return ""
}

type RestoreTape struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	TapeId  int64          `protobuf:"varint,1,opt,name=tape_id,json=tapeId,proto3" json:"tape_id,omitempty"`
	Barcode string         `protobuf:"bytes,2,opt,name=barcode,proto3" json:"barcode,omitempty"`
	Status  CopyStatus     `protobuf:"varint,17,opt,name=status,proto3,enum=copy_status.CopyStatus" json:"status,omitempty"`
	Files   []*RestoreFile `protobuf:"bytes,18,rep,name=files,proto3" json:"files,omitempty"`
}

func (x *RestoreTape) Reset() {
	*x = RestoreTape{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[6]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *RestoreTape) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RestoreTape) ProtoMessage() {}

func (x *RestoreTape) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[6]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RestoreTape.ProtoReflect.Descriptor instead.
func (*RestoreTape) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{6}
}

func (x *RestoreTape) GetTapeId() int64 {
	if x != nil {
		return x.TapeId
	}
	return 0
}

func (x *RestoreTape) GetBarcode() string {
	if x != nil {
		return x.Barcode
	}
	return ""
}

func (x *RestoreTape) GetStatus() CopyStatus {
	if x != nil {
		return x.Status
	}
	return CopyStatus_DRAFT
}

func (x *RestoreTape) GetFiles() []*RestoreFile {
	if x != nil {
		return x.Files
	}
	return nil
}

type JobRestoreState struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Step  JobRestoreStep `protobuf:"varint,1,opt,name=step,proto3,enum=job_restore.JobRestoreStep" json:"step,omitempty"`
	Tapes []*RestoreTape `protobuf:"bytes,2,rep,name=tapes,proto3" json:"tapes,omitempty"`
}

func (x *JobRestoreState) Reset() {
	*x = JobRestoreState{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[7]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JobRestoreState) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JobRestoreState) ProtoMessage() {}

func (x *JobRestoreState) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[7]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JobRestoreState.ProtoReflect.Descriptor instead.
func (*JobRestoreState) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{7}
}

func (x *JobRestoreState) GetStep() JobRestoreStep {
	if x != nil {
		return x.Step
	}
	return JobRestoreStep_PENDING
}

func (x *JobRestoreState) GetTapes() []*RestoreTape {
	if x != nil {
		return x.Tapes
	}
	return nil
}

type JobRestoreDisplay struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	CopiedBytes int64  `protobuf:"varint,1,opt,name=copied_bytes,json=copiedBytes,proto3" json:"copied_bytes,omitempty"`
	CopiedFiles int64  `protobuf:"varint,2,opt,name=copied_files,json=copiedFiles,proto3" json:"copied_files,omitempty"`
	TotalBytes  int64  `protobuf:"varint,3,opt,name=total_bytes,json=totalBytes,proto3" json:"total_bytes,omitempty"`
	TotalFiles  int64  `protobuf:"varint,4,opt,name=total_files,json=totalFiles,proto3" json:"total_files,omitempty"`
	Speed       *int64 `protobuf:"varint,5,opt,name=speed,proto3,oneof" json:"speed,omitempty"`
	StartTime   int64  `protobuf:"varint,6,opt,name=start_time,json=startTime,proto3" json:"start_time,omitempty"`
}

func (x *JobRestoreDisplay) Reset() {
	*x = JobRestoreDisplay{}
	if protoimpl.UnsafeEnabled {
		mi := &file_job_restore_proto_msgTypes[8]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JobRestoreDisplay) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JobRestoreDisplay) ProtoMessage() {}

func (x *JobRestoreDisplay) ProtoReflect() protoreflect.Message {
	mi := &file_job_restore_proto_msgTypes[8]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JobRestoreDisplay.ProtoReflect.Descriptor instead.
func (*JobRestoreDisplay) Descriptor() ([]byte, []int) {
	return file_job_restore_proto_rawDescGZIP(), []int{8}
}

func (x *JobRestoreDisplay) GetCopiedBytes() int64 {
	if x != nil {
		return x.CopiedBytes
	}
	return 0
}

func (x *JobRestoreDisplay) GetCopiedFiles() int64 {
	if x != nil {
		return x.CopiedFiles
	}
	return 0
}

func (x *JobRestoreDisplay) GetTotalBytes() int64 {
	if x != nil {
		return x.TotalBytes
	}
	return 0
}

func (x *JobRestoreDisplay) GetTotalFiles() int64 {
	if x != nil {
		return x.TotalFiles
	}
	return 0
}

func (x *JobRestoreDisplay) GetSpeed() int64 {
	if x != nil && x.Speed != nil {
		return *x.Speed
	}
	return 0
}

func (x *JobRestoreDisplay) GetStartTime() int64 {
	if x != nil {
		return x.StartTime
	}
	return 0
}

var File_job_restore_proto protoreflect.FileDescriptor

var file_job_restore_proto_rawDesc = []byte{
	0x0a, 0x11, 0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x12, 0x0b, 0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65,
	0x1a, 0x11, 0x63, 0x6f, 0x70, 0x79, 0x5f, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x22, 0x2c, 0x0a, 0x0f, 0x4a, 0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72,
	0x65, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x12, 0x19, 0x0a, 0x08, 0x66, 0x69, 0x6c, 0x65, 0x5f, 0x69,
	0x64, 0x73, 0x18, 0x01, 0x20, 0x03, 0x28, 0x03, 0x52, 0x07, 0x66, 0x69, 0x6c, 0x65, 0x49, 0x64,
	0x73, 0x22, 0xf7, 0x01, 0x0a, 0x17, 0x4a, 0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65,
	0x44, 0x69, 0x73, 0x70, 0x61, 0x74, 0x63, 0x68, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x12, 0x4d, 0x0a,
	0x0d, 0x77, 0x61, 0x69, 0x74, 0x5f, 0x66, 0x6f, 0x72, 0x5f, 0x74, 0x61, 0x70, 0x65, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x0b, 0x32, 0x27, 0x2e, 0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65, 0x73, 0x74, 0x6f,
	0x72, 0x65, 0x2e, 0x4a, 0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x57, 0x61, 0x69,
	0x74, 0x46, 0x6f, 0x72, 0x54, 0x61, 0x70, 0x65, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x48, 0x00, 0x52,
	0x0b, 0x77, 0x61, 0x69, 0x74, 0x46, 0x6f, 0x72, 0x54, 0x61, 0x70, 0x65, 0x12, 0x3f, 0x0a, 0x07,
	0x63, 0x6f, 0x70, 0x79, 0x69, 0x6e, 0x67, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x23, 0x2e,
	0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x2e, 0x4a, 0x6f, 0x62, 0x52,
	0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x43, 0x6f, 0x70, 0x79, 0x69, 0x6e, 0x67, 0x50, 0x61, 0x72,
	0x61, 0x6d, 0x48, 0x00, 0x52, 0x07, 0x63, 0x6f, 0x70, 0x79, 0x69, 0x6e, 0x67, 0x12, 0x43, 0x0a,
	0x08, 0x66, 0x69, 0x6e, 0x69, 0x73, 0x68, 0x65, 0x64, 0x18, 0xff, 0x01, 0x20, 0x01, 0x28, 0x0b,
	0x32, 0x24, 0x2e, 0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x2e, 0x4a,
	0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x46, 0x69, 0x6e, 0x69, 0x73, 0x68, 0x65,
	0x64, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x48, 0x00, 0x52, 0x08, 0x66, 0x69, 0x6e, 0x69, 0x73, 0x68,
	0x65, 0x64, 0x42, 0x07, 0x0a, 0x05, 0x70, 0x61, 0x72, 0x61, 0x6d, 0x22, 0x1c, 0x0a, 0x1a, 0x4a,
	0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x57, 0x61, 0x69, 0x74, 0x46, 0x6f, 0x72,
	0x54, 0x61, 0x70, 0x65, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x22, 0x30, 0x0a, 0x16, 0x4a, 0x6f, 0x62,
	0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x43, 0x6f, 0x70, 0x79, 0x69, 0x6e, 0x67, 0x50, 0x61,
	0x72, 0x61, 0x6d, 0x12, 0x16, 0x0a, 0x06, 0x64, 0x65, 0x76, 0x69, 0x63, 0x65, 0x18, 0x01, 0x20,
	0x01, 0x28, 0x09, 0x52, 0x06, 0x64, 0x65, 0x76, 0x69, 0x63, 0x65, 0x22, 0x19, 0x0a, 0x17, 0x4a,
	0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x46, 0x69, 0x6e, 0x69, 0x73, 0x68, 0x65,
	0x64, 0x50, 0x61, 0x72, 0x61, 0x6d, 0x22, 0xf7, 0x01, 0x0a, 0x0b, 0x52, 0x65, 0x73, 0x74, 0x6f,
	0x72, 0x65, 0x46, 0x69, 0x6c, 0x65, 0x12, 0x17, 0x0a, 0x07, 0x66, 0x69, 0x6c, 0x65, 0x5f, 0x69,
	0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x03, 0x52, 0x06, 0x66, 0x69, 0x6c, 0x65, 0x49, 0x64, 0x12,
	0x17, 0x0a, 0x07, 0x74, 0x61, 0x70, 0x65, 0x5f, 0x69, 0x64, 0x18, 0x02, 0x20, 0x01, 0x28, 0x03,
	0x52, 0x06, 0x74, 0x61, 0x70, 0x65, 0x49, 0x64, 0x12, 0x1f, 0x0a, 0x0b, 0x70, 0x6f, 0x73, 0x69,
	0x74, 0x69, 0x6f, 0x6e, 0x5f, 0x69, 0x64, 0x18, 0x03, 0x20, 0x01, 0x28, 0x03, 0x52, 0x0a, 0x70,
	0x6f, 0x73, 0x69, 0x74, 0x69, 0x6f, 0x6e, 0x49, 0x64, 0x12, 0x2f, 0x0a, 0x06, 0x73, 0x74, 0x61,
	0x74, 0x75, 0x73, 0x18, 0x11, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x17, 0x2e, 0x63, 0x6f, 0x70, 0x79,
	0x5f, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x2e, 0x43, 0x6f, 0x70, 0x79, 0x53, 0x74, 0x61, 0x74,
	0x75, 0x73, 0x52, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x12, 0x12, 0x0a, 0x04, 0x73, 0x69,
	0x7a, 0x65, 0x18, 0x12, 0x20, 0x01, 0x28, 0x03, 0x52, 0x04, 0x73, 0x69, 0x7a, 0x65, 0x12, 0x12,
	0x0a, 0x04, 0x68, 0x61, 0x73, 0x68, 0x18, 0x13, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x04, 0x68, 0x61,
	0x73, 0x68, 0x12, 0x1b, 0x0a, 0x09, 0x74, 0x61, 0x70, 0x65, 0x5f, 0x70, 0x61, 0x74, 0x68, 0x18,
	0x21, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x74, 0x61, 0x70, 0x65, 0x50, 0x61, 0x74, 0x68, 0x12,
	0x1f, 0x0a, 0x0b, 0x74, 0x61, 0x72, 0x67, 0x65, 0x74, 0x5f, 0x70, 0x61, 0x74, 0x68, 0x18, 0x22,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x0a, 0x74, 0x61, 0x72, 0x67, 0x65, 0x74, 0x50, 0x61, 0x74, 0x68,
	0x22, 0xa1, 0x01, 0x0a, 0x0b, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x54, 0x61, 0x70, 0x65,
	0x12, 0x17, 0x0a, 0x07, 0x74, 0x61, 0x70, 0x65, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x03, 0x52, 0x06, 0x74, 0x61, 0x70, 0x65, 0x49, 0x64, 0x12, 0x18, 0x0a, 0x07, 0x62, 0x61, 0x72,
	0x63, 0x6f, 0x64, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x62, 0x61, 0x72, 0x63,
	0x6f, 0x64, 0x65, 0x12, 0x2f, 0x0a, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x18, 0x11, 0x20,
	0x01, 0x28, 0x0e, 0x32, 0x17, 0x2e, 0x63, 0x6f, 0x70, 0x79, 0x5f, 0x73, 0x74, 0x61, 0x74, 0x75,
	0x73, 0x2e, 0x43, 0x6f, 0x70, 0x79, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x52, 0x06, 0x73, 0x74,
	0x61, 0x74, 0x75, 0x73, 0x12, 0x2e, 0x0a, 0x05, 0x66, 0x69, 0x6c, 0x65, 0x73, 0x18, 0x12, 0x20,
	0x03, 0x28, 0x0b, 0x32, 0x18, 0x2e, 0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65, 0x73, 0x74, 0x6f, 0x72,
	0x65, 0x2e, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x46, 0x69, 0x6c, 0x65, 0x52, 0x05, 0x66,
	0x69, 0x6c, 0x65, 0x73, 0x22, 0x72, 0x0a, 0x0f, 0x4a, 0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f,
	0x72, 0x65, 0x53, 0x74, 0x61, 0x74, 0x65, 0x12, 0x2f, 0x0a, 0x04, 0x73, 0x74, 0x65, 0x70, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x1b, 0x2e, 0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65, 0x73, 0x74,
	0x6f, 0x72, 0x65, 0x2e, 0x4a, 0x6f, 0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x53, 0x74,
	0x65, 0x70, 0x52, 0x04, 0x73, 0x74, 0x65, 0x70, 0x12, 0x2e, 0x0a, 0x05, 0x74, 0x61, 0x70, 0x65,
	0x73, 0x18, 0x02, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x18, 0x2e, 0x6a, 0x6f, 0x62, 0x5f, 0x72, 0x65,
	0x73, 0x74, 0x6f, 0x72, 0x65, 0x2e, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x54, 0x61, 0x70,
	0x65, 0x52, 0x05, 0x74, 0x61, 0x70, 0x65, 0x73, 0x22, 0xdf, 0x01, 0x0a, 0x11, 0x4a, 0x6f, 0x62,
	0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x44, 0x69, 0x73, 0x70, 0x6c, 0x61, 0x79, 0x12, 0x21,
	0x0a, 0x0c, 0x63, 0x6f, 0x70, 0x69, 0x65, 0x64, 0x5f, 0x62, 0x79, 0x74, 0x65, 0x73, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x03, 0x52, 0x0b, 0x63, 0x6f, 0x70, 0x69, 0x65, 0x64, 0x42, 0x79, 0x74, 0x65,
	0x73, 0x12, 0x21, 0x0a, 0x0c, 0x63, 0x6f, 0x70, 0x69, 0x65, 0x64, 0x5f, 0x66, 0x69, 0x6c, 0x65,
	0x73, 0x18, 0x02, 0x20, 0x01, 0x28, 0x03, 0x52, 0x0b, 0x63, 0x6f, 0x70, 0x69, 0x65, 0x64, 0x46,
	0x69, 0x6c, 0x65, 0x73, 0x12, 0x1f, 0x0a, 0x0b, 0x74, 0x6f, 0x74, 0x61, 0x6c, 0x5f, 0x62, 0x79,
	0x74, 0x65, 0x73, 0x18, 0x03, 0x20, 0x01, 0x28, 0x03, 0x52, 0x0a, 0x74, 0x6f, 0x74, 0x61, 0x6c,
	0x42, 0x79, 0x74, 0x65, 0x73, 0x12, 0x1f, 0x0a, 0x0b, 0x74, 0x6f, 0x74, 0x61, 0x6c, 0x5f, 0x66,
	0x69, 0x6c, 0x65, 0x73, 0x18, 0x04, 0x20, 0x01, 0x28, 0x03, 0x52, 0x0a, 0x74, 0x6f, 0x74, 0x61,
	0x6c, 0x46, 0x69, 0x6c, 0x65, 0x73, 0x12, 0x19, 0x0a, 0x05, 0x73, 0x70, 0x65, 0x65, 0x64, 0x18,
	0x05, 0x20, 0x01, 0x28, 0x03, 0x48, 0x00, 0x52, 0x05, 0x73, 0x70, 0x65, 0x65, 0x64, 0x88, 0x01,
	0x01, 0x12, 0x1d, 0x0a, 0x0a, 0x73, 0x74, 0x61, 0x72, 0x74, 0x5f, 0x74, 0x69, 0x6d, 0x65, 0x18,
	0x06, 0x20, 0x01, 0x28, 0x03, 0x52, 0x09, 0x73, 0x74, 0x61, 0x72, 0x74, 0x54, 0x69, 0x6d, 0x65,
	0x42, 0x08, 0x0a, 0x06, 0x5f, 0x73, 0x70, 0x65, 0x65, 0x64, 0x2a, 0x4c, 0x0a, 0x0e, 0x4a, 0x6f,
	0x62, 0x52, 0x65, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x53, 0x74, 0x65, 0x70, 0x12, 0x0b, 0x0a, 0x07,
	0x50, 0x45, 0x4e, 0x44, 0x49, 0x4e, 0x47, 0x10, 0x00, 0x12, 0x11, 0x0a, 0x0d, 0x57, 0x41, 0x49,
	0x54, 0x5f, 0x46, 0x4f, 0x52, 0x5f, 0x54, 0x41, 0x50, 0x45, 0x10, 0x01, 0x12, 0x0b, 0x0a, 0x07,
	0x43, 0x4f, 0x50, 0x59, 0x49, 0x4e, 0x47, 0x10, 0x02, 0x12, 0x0d, 0x0a, 0x08, 0x46, 0x49, 0x4e,
	0x49, 0x53, 0x48, 0x45, 0x44, 0x10, 0xff, 0x01, 0x42, 0x23, 0x5a, 0x21, 0x67, 0x69, 0x74, 0x68,
	0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x73, 0x61, 0x6d, 0x75, 0x65, 0x6c, 0x6e, 0x63, 0x75,
	0x69, 0x2f, 0x79, 0x61, 0x74, 0x6d, 0x2f, 0x65, 0x6e, 0x74, 0x69, 0x74, 0x79, 0x62, 0x06, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_job_restore_proto_rawDescOnce sync.Once
	file_job_restore_proto_rawDescData = file_job_restore_proto_rawDesc
)

func file_job_restore_proto_rawDescGZIP() []byte {
	file_job_restore_proto_rawDescOnce.Do(func() {
		file_job_restore_proto_rawDescData = protoimpl.X.CompressGZIP(file_job_restore_proto_rawDescData)
	})
	return file_job_restore_proto_rawDescData
}

var file_job_restore_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_job_restore_proto_msgTypes = make([]protoimpl.MessageInfo, 9)
var file_job_restore_proto_goTypes = []interface{}{
	(JobRestoreStep)(0),                // 0: job_restore.JobRestoreStep
	(*JobRestoreParam)(nil),            // 1: job_restore.JobRestoreParam
	(*JobRestoreDispatchParam)(nil),    // 2: job_restore.JobRestoreDispatchParam
	(*JobRestoreWaitForTapeParam)(nil), // 3: job_restore.JobRestoreWaitForTapeParam
	(*JobRestoreCopyingParam)(nil),     // 4: job_restore.JobRestoreCopyingParam
	(*JobRestoreFinishedParam)(nil),    // 5: job_restore.JobRestoreFinishedParam
	(*RestoreFile)(nil),                // 6: job_restore.RestoreFile
	(*RestoreTape)(nil),                // 7: job_restore.RestoreTape
	(*JobRestoreState)(nil),            // 8: job_restore.JobRestoreState
	(*JobRestoreDisplay)(nil),          // 9: job_restore.JobRestoreDisplay
	(CopyStatus)(0),                    // 10: copy_status.CopyStatus
}
var file_job_restore_proto_depIdxs = []int32{
	3,  // 0: job_restore.JobRestoreDispatchParam.wait_for_tape:type_name -> job_restore.JobRestoreWaitForTapeParam
	4,  // 1: job_restore.JobRestoreDispatchParam.copying:type_name -> job_restore.JobRestoreCopyingParam
	5,  // 2: job_restore.JobRestoreDispatchParam.finished:type_name -> job_restore.JobRestoreFinishedParam
	10, // 3: job_restore.RestoreFile.status:type_name -> copy_status.CopyStatus
	10, // 4: job_restore.RestoreTape.status:type_name -> copy_status.CopyStatus
	6,  // 5: job_restore.RestoreTape.files:type_name -> job_restore.RestoreFile
	0,  // 6: job_restore.JobRestoreState.step:type_name -> job_restore.JobRestoreStep
	7,  // 7: job_restore.JobRestoreState.tapes:type_name -> job_restore.RestoreTape
	8,  // [8:8] is the sub-list for method output_type
	8,  // [8:8] is the sub-list for method input_type
	8,  // [8:8] is the sub-list for extension type_name
	8,  // [8:8] is the sub-list for extension extendee
	0,  // [0:8] is the sub-list for field type_name
}

func init() { file_job_restore_proto_init() }
func file_job_restore_proto_init() {
	if File_job_restore_proto != nil {
		return
	}
	file_copy_status_proto_init()
	if !protoimpl.UnsafeEnabled {
		file_job_restore_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*JobRestoreParam); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*JobRestoreDispatchParam); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*JobRestoreWaitForTapeParam); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*JobRestoreCopyingParam); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*JobRestoreFinishedParam); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*RestoreFile); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[6].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*RestoreTape); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[7].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*JobRestoreState); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_job_restore_proto_msgTypes[8].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*JobRestoreDisplay); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	file_job_restore_proto_msgTypes[1].OneofWrappers = []interface{}{
		(*JobRestoreDispatchParam_WaitForTape)(nil),
		(*JobRestoreDispatchParam_Copying)(nil),
		(*JobRestoreDispatchParam_Finished)(nil),
	}
	file_job_restore_proto_msgTypes[8].OneofWrappers = []interface{}{}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_job_restore_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   9,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_job_restore_proto_goTypes,
		DependencyIndexes: file_job_restore_proto_depIdxs,
		EnumInfos:         file_job_restore_proto_enumTypes,
		MessageInfos:      file_job_restore_proto_msgTypes,
	}.Build()
	File_job_restore_proto = out.File
	file_job_restore_proto_rawDesc = nil
	file_job_restore_proto_goTypes = nil
	file_job_restore_proto_depIdxs = nil
}
