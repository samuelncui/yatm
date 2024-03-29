// @generated by protobuf-ts 2.9.1
// @generated from protobuf file "source.proto" (package "source", syntax proto3)
// tslint:disable
import type { BinaryWriteOptions } from "@protobuf-ts/runtime";
import type { IBinaryWriter } from "@protobuf-ts/runtime";
import { WireType } from "@protobuf-ts/runtime";
import type { BinaryReadOptions } from "@protobuf-ts/runtime";
import type { IBinaryReader } from "@protobuf-ts/runtime";
import { UnknownFieldHandler } from "@protobuf-ts/runtime";
import type { PartialMessage } from "@protobuf-ts/runtime";
import { reflectionMergePartial } from "@protobuf-ts/runtime";
import { MESSAGE_TYPE } from "@protobuf-ts/runtime";
import { MessageType } from "@protobuf-ts/runtime";
import { CopyStatus } from "./copy_status";
/**
 * @generated from protobuf message source.SourceFile
 */
export interface SourceFile {
    /**
     * @generated from protobuf field: string path = 1;
     */
    path: string;
    /**
     * @generated from protobuf field: string parent_path = 2;
     */
    parentPath: string;
    /**
     * @generated from protobuf field: string name = 3;
     */
    name: string;
    /**
     * @generated from protobuf field: int64 mode = 17;
     */
    mode: bigint;
    /**
     * @generated from protobuf field: int64 mod_time = 18;
     */
    modTime: bigint;
    /**
     * @generated from protobuf field: int64 size = 19;
     */
    size: bigint;
}
/**
 * @generated from protobuf message source.Source
 */
export interface Source {
    /**
     * @generated from protobuf field: string base = 1;
     */
    base: string;
    /**
     * @generated from protobuf field: repeated string path = 2;
     */
    path: string[];
}
/**
 * @generated from protobuf message source.SourceState
 */
export interface SourceState {
    /**
     * @generated from protobuf field: source.Source source = 1;
     */
    source?: Source;
    /**
     * @generated from protobuf field: int64 size = 2;
     */
    size: bigint;
    /**
     * @generated from protobuf field: copy_status.CopyStatus status = 3;
     */
    status: CopyStatus;
    /**
     * @generated from protobuf field: optional string message = 4;
     */
    message?: string;
}
// @generated message type with reflection information, may provide speed optimized methods
class SourceFile$Type extends MessageType<SourceFile> {
    constructor() {
        super("source.SourceFile", [
            { no: 1, name: "path", kind: "scalar", T: 9 /*ScalarType.STRING*/ },
            { no: 2, name: "parent_path", kind: "scalar", T: 9 /*ScalarType.STRING*/ },
            { no: 3, name: "name", kind: "scalar", T: 9 /*ScalarType.STRING*/ },
            { no: 17, name: "mode", kind: "scalar", T: 3 /*ScalarType.INT64*/, L: 0 /*LongType.BIGINT*/ },
            { no: 18, name: "mod_time", kind: "scalar", T: 3 /*ScalarType.INT64*/, L: 0 /*LongType.BIGINT*/ },
            { no: 19, name: "size", kind: "scalar", T: 3 /*ScalarType.INT64*/, L: 0 /*LongType.BIGINT*/ }
        ]);
    }
    create(value?: PartialMessage<SourceFile>): SourceFile {
        const message = { path: "", parentPath: "", name: "", mode: 0n, modTime: 0n, size: 0n };
        globalThis.Object.defineProperty(message, MESSAGE_TYPE, { enumerable: false, value: this });
        if (value !== undefined)
            reflectionMergePartial<SourceFile>(this, message, value);
        return message;
    }
    internalBinaryRead(reader: IBinaryReader, length: number, options: BinaryReadOptions, target?: SourceFile): SourceFile {
        let message = target ?? this.create(), end = reader.pos + length;
        while (reader.pos < end) {
            let [fieldNo, wireType] = reader.tag();
            switch (fieldNo) {
                case /* string path */ 1:
                    message.path = reader.string();
                    break;
                case /* string parent_path */ 2:
                    message.parentPath = reader.string();
                    break;
                case /* string name */ 3:
                    message.name = reader.string();
                    break;
                case /* int64 mode */ 17:
                    message.mode = reader.int64().toBigInt();
                    break;
                case /* int64 mod_time */ 18:
                    message.modTime = reader.int64().toBigInt();
                    break;
                case /* int64 size */ 19:
                    message.size = reader.int64().toBigInt();
                    break;
                default:
                    let u = options.readUnknownField;
                    if (u === "throw")
                        throw new globalThis.Error(`Unknown field ${fieldNo} (wire type ${wireType}) for ${this.typeName}`);
                    let d = reader.skip(wireType);
                    if (u !== false)
                        (u === true ? UnknownFieldHandler.onRead : u)(this.typeName, message, fieldNo, wireType, d);
            }
        }
        return message;
    }
    internalBinaryWrite(message: SourceFile, writer: IBinaryWriter, options: BinaryWriteOptions): IBinaryWriter {
        /* string path = 1; */
        if (message.path !== "")
            writer.tag(1, WireType.LengthDelimited).string(message.path);
        /* string parent_path = 2; */
        if (message.parentPath !== "")
            writer.tag(2, WireType.LengthDelimited).string(message.parentPath);
        /* string name = 3; */
        if (message.name !== "")
            writer.tag(3, WireType.LengthDelimited).string(message.name);
        /* int64 mode = 17; */
        if (message.mode !== 0n)
            writer.tag(17, WireType.Varint).int64(message.mode);
        /* int64 mod_time = 18; */
        if (message.modTime !== 0n)
            writer.tag(18, WireType.Varint).int64(message.modTime);
        /* int64 size = 19; */
        if (message.size !== 0n)
            writer.tag(19, WireType.Varint).int64(message.size);
        let u = options.writeUnknownFields;
        if (u !== false)
            (u == true ? UnknownFieldHandler.onWrite : u)(this.typeName, message, writer);
        return writer;
    }
}
/**
 * @generated MessageType for protobuf message source.SourceFile
 */
export const SourceFile = new SourceFile$Type();
// @generated message type with reflection information, may provide speed optimized methods
class Source$Type extends MessageType<Source> {
    constructor() {
        super("source.Source", [
            { no: 1, name: "base", kind: "scalar", T: 9 /*ScalarType.STRING*/ },
            { no: 2, name: "path", kind: "scalar", repeat: 2 /*RepeatType.UNPACKED*/, T: 9 /*ScalarType.STRING*/ }
        ]);
    }
    create(value?: PartialMessage<Source>): Source {
        const message = { base: "", path: [] };
        globalThis.Object.defineProperty(message, MESSAGE_TYPE, { enumerable: false, value: this });
        if (value !== undefined)
            reflectionMergePartial<Source>(this, message, value);
        return message;
    }
    internalBinaryRead(reader: IBinaryReader, length: number, options: BinaryReadOptions, target?: Source): Source {
        let message = target ?? this.create(), end = reader.pos + length;
        while (reader.pos < end) {
            let [fieldNo, wireType] = reader.tag();
            switch (fieldNo) {
                case /* string base */ 1:
                    message.base = reader.string();
                    break;
                case /* repeated string path */ 2:
                    message.path.push(reader.string());
                    break;
                default:
                    let u = options.readUnknownField;
                    if (u === "throw")
                        throw new globalThis.Error(`Unknown field ${fieldNo} (wire type ${wireType}) for ${this.typeName}`);
                    let d = reader.skip(wireType);
                    if (u !== false)
                        (u === true ? UnknownFieldHandler.onRead : u)(this.typeName, message, fieldNo, wireType, d);
            }
        }
        return message;
    }
    internalBinaryWrite(message: Source, writer: IBinaryWriter, options: BinaryWriteOptions): IBinaryWriter {
        /* string base = 1; */
        if (message.base !== "")
            writer.tag(1, WireType.LengthDelimited).string(message.base);
        /* repeated string path = 2; */
        for (let i = 0; i < message.path.length; i++)
            writer.tag(2, WireType.LengthDelimited).string(message.path[i]);
        let u = options.writeUnknownFields;
        if (u !== false)
            (u == true ? UnknownFieldHandler.onWrite : u)(this.typeName, message, writer);
        return writer;
    }
}
/**
 * @generated MessageType for protobuf message source.Source
 */
export const Source = new Source$Type();
// @generated message type with reflection information, may provide speed optimized methods
class SourceState$Type extends MessageType<SourceState> {
    constructor() {
        super("source.SourceState", [
            { no: 1, name: "source", kind: "message", T: () => Source },
            { no: 2, name: "size", kind: "scalar", T: 3 /*ScalarType.INT64*/, L: 0 /*LongType.BIGINT*/ },
            { no: 3, name: "status", kind: "enum", T: () => ["copy_status.CopyStatus", CopyStatus] },
            { no: 4, name: "message", kind: "scalar", opt: true, T: 9 /*ScalarType.STRING*/ }
        ]);
    }
    create(value?: PartialMessage<SourceState>): SourceState {
        const message = { size: 0n, status: 0 };
        globalThis.Object.defineProperty(message, MESSAGE_TYPE, { enumerable: false, value: this });
        if (value !== undefined)
            reflectionMergePartial<SourceState>(this, message, value);
        return message;
    }
    internalBinaryRead(reader: IBinaryReader, length: number, options: BinaryReadOptions, target?: SourceState): SourceState {
        let message = target ?? this.create(), end = reader.pos + length;
        while (reader.pos < end) {
            let [fieldNo, wireType] = reader.tag();
            switch (fieldNo) {
                case /* source.Source source */ 1:
                    message.source = Source.internalBinaryRead(reader, reader.uint32(), options, message.source);
                    break;
                case /* int64 size */ 2:
                    message.size = reader.int64().toBigInt();
                    break;
                case /* copy_status.CopyStatus status */ 3:
                    message.status = reader.int32();
                    break;
                case /* optional string message */ 4:
                    message.message = reader.string();
                    break;
                default:
                    let u = options.readUnknownField;
                    if (u === "throw")
                        throw new globalThis.Error(`Unknown field ${fieldNo} (wire type ${wireType}) for ${this.typeName}`);
                    let d = reader.skip(wireType);
                    if (u !== false)
                        (u === true ? UnknownFieldHandler.onRead : u)(this.typeName, message, fieldNo, wireType, d);
            }
        }
        return message;
    }
    internalBinaryWrite(message: SourceState, writer: IBinaryWriter, options: BinaryWriteOptions): IBinaryWriter {
        /* source.Source source = 1; */
        if (message.source)
            Source.internalBinaryWrite(message.source, writer.tag(1, WireType.LengthDelimited).fork(), options).join();
        /* int64 size = 2; */
        if (message.size !== 0n)
            writer.tag(2, WireType.Varint).int64(message.size);
        /* copy_status.CopyStatus status = 3; */
        if (message.status !== 0)
            writer.tag(3, WireType.Varint).int32(message.status);
        /* optional string message = 4; */
        if (message.message !== undefined)
            writer.tag(4, WireType.LengthDelimited).string(message.message);
        let u = options.writeUnknownFields;
        if (u !== false)
            (u == true ? UnknownFieldHandler.onWrite : u)(this.typeName, message, writer);
        return writer;
    }
}
/**
 * @generated MessageType for protobuf message source.SourceState
 */
export const SourceState = new SourceState$Type();
