# Vendored upstream SDK

Source: https://github.com/a2aproject/a2a-go/tree/7bd24a21e2d9cffad86ec7ba915abcef389ba93f
Tag: v1.0.0-alpha.3; Apache-2.0 (LICENSE included).

The published module declares an invalid Go major-version suffix `/v1`. Go rejects downloading it. This snapshot changes the module/import path to `github.com/a2aproject/a2a-go-v1` and applies the documented compatibility patches below. Tests/examples/docs and unused tool files are omitted. Go 0.3 compatibility remains an ordinary upstream module dependency. Replace this snapshot when upstream publishes a consumable module. The v1 SDK is alpha; our transport/lifecycle regression tests are required before upgrades.

Compatibility patch: v1 generated protobuf descriptor filename changed from `a2a.proto` to `a2a-v1.proto` (length prefix adjusted), avoiding a global descriptor registry collision when v0 and v1 gRPC bindings are loaded together. Message/service names and wire fields are unchanged.

Compatibility patch: filled the alpha SDK 0.3 JSON-RPC `tasks/list` client/server gap, translating task state and task payloads through the existing upstream converters. Covered by integration tests across JSON-RPC 0.3/1.0, REST and gRPC.

Wire conformance patch: v1 JSON enum values now use ProtoJSON `ROLE_*` and `TASK_STATE_*` names; security union fields use the normative `*SecurityScheme` names; version/required-extension errors map to -32009/-32008. Early-alpha JSON values remain readable for existing records. Source: current A2A specification §§4.5, 5.4, 5.5, independently checked against specification/a2a.proto.

Transport patch: gRPC client metadata construction preserves pre-existing outgoing authentication metadata. The platform wraps REST requests with a ServiceParams call context because the alpha REST handler omitted header propagation.
