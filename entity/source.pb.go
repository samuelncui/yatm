// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.28.1
// 	protoc        v3.21.10
// source: source.proto

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

type SourceFile struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Path       string `protobuf:"bytes,1,opt,name=path,proto3" json:"path,omitempty"`
	ParentPath string `protobuf:"bytes,2,opt,name=parent_path,json=parentPath,proto3" json:"parent_path,omitempty"`
	Name       string `protobuf:"bytes,3,opt,name=name,proto3" json:"name,omitempty"`
	Mode       int64  `protobuf:"varint,17,opt,name=mode,proto3" json:"mode,omitempty"`
	ModTime    int64  `protobuf:"varint,18,opt,name=mod_time,json=modTime,proto3" json:"mod_time,omitempty"`
	Size       int64  `protobuf:"varint,19,opt,name=size,proto3" json:"size,omitempty"`
}

func (x *SourceFile) Reset() {
	*x = SourceFile{}
	if protoimpl.UnsafeEnabled {
		mi := &file_source_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *SourceFile) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SourceFile) ProtoMessage() {}

func (x *SourceFile) ProtoReflect() protoreflect.Message {
	mi := &file_source_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SourceFile.ProtoReflect.Descriptor instead.
func (*SourceFile) Descriptor() ([]byte, []int) {
	return file_source_proto_rawDescGZIP(), []int{0}
}

func (x *SourceFile) GetPath() string {
	if x != nil {
		return x.Path
	}
	return ""
}

func (x *SourceFile) GetParentPath() string {
	if x != nil {
		return x.ParentPath
	}
	return ""
}

func (x *SourceFile) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

func (x *SourceFile) GetMode() int64 {
	if x != nil {
		return x.Mode
	}
	return 0
}

func (x *SourceFile) GetModTime() int64 {
	if x != nil {
		return x.ModTime
	}
	return 0
}

func (x *SourceFile) GetSize() int64 {
	if x != nil {
		return x.Size
	}
	return 0
}

type Source struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Base string   `protobuf:"bytes,1,opt,name=base,proto3" json:"base,omitempty"`
	Path []string `protobuf:"bytes,2,rep,name=path,proto3" json:"path,omitempty"`
}

func (x *Source) Reset() {
	*x = Source{}
	if protoimpl.UnsafeEnabled {
		mi := &file_source_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *Source) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Source) ProtoMessage() {}

func (x *Source) ProtoReflect() protoreflect.Message {
	mi := &file_source_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Source.ProtoReflect.Descriptor instead.
func (*Source) Descriptor() ([]byte, []int) {
	return file_source_proto_rawDescGZIP(), []int{1}
}

func (x *Source) GetBase() string {
	if x != nil {
		return x.Base
	}
	return ""
}

func (x *Source) GetPath() []string {
	if x != nil {
		return x.Path
	}
	return nil
}

type SourceState struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Source  *Source    `protobuf:"bytes,1,opt,name=source,proto3" json:"source,omitempty"`
	Size    int64      `protobuf:"varint,2,opt,name=size,proto3" json:"size,omitempty"`
	Status  CopyStatus `protobuf:"varint,3,opt,name=status,proto3,enum=copy_status.CopyStatus" json:"status,omitempty"`
	Message *string    `protobuf:"bytes,4,opt,name=message,proto3,oneof" json:"message,omitempty"`
}

func (x *SourceState) Reset() {
	*x = SourceState{}
	if protoimpl.UnsafeEnabled {
		mi := &file_source_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *SourceState) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SourceState) ProtoMessage() {}

func (x *SourceState) ProtoReflect() protoreflect.Message {
	mi := &file_source_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SourceState.ProtoReflect.Descriptor instead.
func (*SourceState) Descriptor() ([]byte, []int) {
	return file_source_proto_rawDescGZIP(), []int{2}
}

func (x *SourceState) GetSource() *Source {
	if x != nil {
		return x.Source
	}
	return nil
}

func (x *SourceState) GetSize() int64 {
	if x != nil {
		return x.Size
	}
	return 0
}

func (x *SourceState) GetStatus() CopyStatus {
	if x != nil {
		return x.Status
	}
	return CopyStatus_DRAFT
}

func (x *SourceState) GetMessage() string {
	if x != nil && x.Message != nil {
		return *x.Message
	}
	return ""
}

var File_source_proto protoreflect.FileDescriptor

var file_source_proto_rawDesc = []byte{
	0x0a, 0x0c, 0x73, 0x6f, 0x75, 0x72, 0x63, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x06,
	0x73, 0x6f, 0x75, 0x72, 0x63, 0x65, 0x1a, 0x11, 0x63, 0x6f, 0x70, 0x79, 0x5f, 0x73, 0x74, 0x61,
	0x74, 0x75, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x98, 0x01, 0x0a, 0x0a, 0x53, 0x6f,
	0x75, 0x72, 0x63, 0x65, 0x46, 0x69, 0x6c, 0x65, 0x12, 0x12, 0x0a, 0x04, 0x70, 0x61, 0x74, 0x68,
	0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x70, 0x61, 0x74, 0x68, 0x12, 0x1f, 0x0a, 0x0b,
	0x70, 0x61, 0x72, 0x65, 0x6e, 0x74, 0x5f, 0x70, 0x61, 0x74, 0x68, 0x18, 0x02, 0x20, 0x01, 0x28,
	0x09, 0x52, 0x0a, 0x70, 0x61, 0x72, 0x65, 0x6e, 0x74, 0x50, 0x61, 0x74, 0x68, 0x12, 0x12, 0x0a,
	0x04, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x6e, 0x61, 0x6d,
	0x65, 0x12, 0x12, 0x0a, 0x04, 0x6d, 0x6f, 0x64, 0x65, 0x18, 0x11, 0x20, 0x01, 0x28, 0x03, 0x52,
	0x04, 0x6d, 0x6f, 0x64, 0x65, 0x12, 0x19, 0x0a, 0x08, 0x6d, 0x6f, 0x64, 0x5f, 0x74, 0x69, 0x6d,
	0x65, 0x18, 0x12, 0x20, 0x01, 0x28, 0x03, 0x52, 0x07, 0x6d, 0x6f, 0x64, 0x54, 0x69, 0x6d, 0x65,
	0x12, 0x12, 0x0a, 0x04, 0x73, 0x69, 0x7a, 0x65, 0x18, 0x13, 0x20, 0x01, 0x28, 0x03, 0x52, 0x04,
	0x73, 0x69, 0x7a, 0x65, 0x22, 0x30, 0x0a, 0x06, 0x53, 0x6f, 0x75, 0x72, 0x63, 0x65, 0x12, 0x12,
	0x0a, 0x04, 0x62, 0x61, 0x73, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x62, 0x61,
	0x73, 0x65, 0x12, 0x12, 0x0a, 0x04, 0x70, 0x61, 0x74, 0x68, 0x18, 0x02, 0x20, 0x03, 0x28, 0x09,
	0x52, 0x04, 0x70, 0x61, 0x74, 0x68, 0x22, 0xa5, 0x01, 0x0a, 0x0b, 0x53, 0x6f, 0x75, 0x72, 0x63,
	0x65, 0x53, 0x74, 0x61, 0x74, 0x65, 0x12, 0x26, 0x0a, 0x06, 0x73, 0x6f, 0x75, 0x72, 0x63, 0x65,
	0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0e, 0x2e, 0x73, 0x6f, 0x75, 0x72, 0x63, 0x65, 0x2e,
	0x53, 0x6f, 0x75, 0x72, 0x63, 0x65, 0x52, 0x06, 0x73, 0x6f, 0x75, 0x72, 0x63, 0x65, 0x12, 0x12,
	0x0a, 0x04, 0x73, 0x69, 0x7a, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x03, 0x52, 0x04, 0x73, 0x69,
	0x7a, 0x65, 0x12, 0x2f, 0x0a, 0x06, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x18, 0x03, 0x20, 0x01,
	0x28, 0x0e, 0x32, 0x17, 0x2e, 0x63, 0x6f, 0x70, 0x79, 0x5f, 0x73, 0x74, 0x61, 0x74, 0x75, 0x73,
	0x2e, 0x43, 0x6f, 0x70, 0x79, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x52, 0x06, 0x73, 0x74, 0x61,
	0x74, 0x75, 0x73, 0x12, 0x1d, 0x0a, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x18, 0x04,
	0x20, 0x01, 0x28, 0x09, 0x48, 0x00, 0x52, 0x07, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x88,
	0x01, 0x01, 0x42, 0x0a, 0x0a, 0x08, 0x5f, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x42, 0x23,
	0x5a, 0x21, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x73, 0x61, 0x6d,
	0x75, 0x65, 0x6c, 0x6e, 0x63, 0x75, 0x69, 0x2f, 0x79, 0x61, 0x74, 0x6d, 0x2f, 0x65, 0x6e, 0x74,
	0x69, 0x74, 0x79, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_source_proto_rawDescOnce sync.Once
	file_source_proto_rawDescData = file_source_proto_rawDesc
)

func file_source_proto_rawDescGZIP() []byte {
	file_source_proto_rawDescOnce.Do(func() {
		file_source_proto_rawDescData = protoimpl.X.CompressGZIP(file_source_proto_rawDescData)
	})
	return file_source_proto_rawDescData
}

var file_source_proto_msgTypes = make([]protoimpl.MessageInfo, 3)
var file_source_proto_goTypes = []interface{}{
	(*SourceFile)(nil),  // 0: source.SourceFile
	(*Source)(nil),      // 1: source.Source
	(*SourceState)(nil), // 2: source.SourceState
	(CopyStatus)(0),     // 3: copy_status.CopyStatus
}
var file_source_proto_depIdxs = []int32{
	1, // 0: source.SourceState.source:type_name -> source.Source
	3, // 1: source.SourceState.status:type_name -> copy_status.CopyStatus
	2, // [2:2] is the sub-list for method output_type
	2, // [2:2] is the sub-list for method input_type
	2, // [2:2] is the sub-list for extension type_name
	2, // [2:2] is the sub-list for extension extendee
	0, // [0:2] is the sub-list for field type_name
}

func init() { file_source_proto_init() }
func file_source_proto_init() {
	if File_source_proto != nil {
		return
	}
	file_copy_status_proto_init()
	if !protoimpl.UnsafeEnabled {
		file_source_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*SourceFile); i {
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
		file_source_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*Source); i {
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
		file_source_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*SourceState); i {
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
	file_source_proto_msgTypes[2].OneofWrappers = []interface{}{}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_source_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   3,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_source_proto_goTypes,
		DependencyIndexes: file_source_proto_depIdxs,
		MessageInfos:      file_source_proto_msgTypes,
	}.Build()
	File_source_proto = out.File
	file_source_proto_rawDesc = nil
	file_source_proto_goTypes = nil
	file_source_proto_depIdxs = nil
}
