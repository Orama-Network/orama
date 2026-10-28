export { AuthClient } from "./client";
export type { DeviceState } from "./client";
export { deviceIdOf, deviceProofMessage, makeDeviceProof, normalizeUserCode } from "./device";
export { deviceRevocationOf, verifyDeviceRevocation, webCryptoVerify } from "./revocation";
export type {
  RevokedBy,
  DeviceRevocation,
  DeviceRevocationProof,
  DeviceRevocationVerdict,
  RevocationSignatureVerifier,
  VerifyDeviceRevocationOptions,
} from "./revocation";
export type {
  DeviceSigner,
  DeviceProof,
  DeviceProofAction,
  DeviceInfo,
  DeviceLink,
  PendingDeviceApproval,
} from "./device";
export type { AuthConfig, WhoAmI, StorageAdapter } from "./types";
export { MemoryStorage, LocalStorageAdapter } from "./types";
