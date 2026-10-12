import {
  type BoardlyCommerceSigner,
  createBoardlySiteVerificationDocument,
} from "../builtin/zenith-platform/commerce/templates/boardly-commerce.server";
import verificationProof from "./boardly-commerce.verification.json";

declare const signer: BoardlyCommerceSigner;

const verificationDocument = createBoardlySiteVerificationDocument(
  verificationProof,
  signer,
);

void verificationDocument.then((document) => {
  const schemaVersion: 2 = document.schemaVersion;
  return schemaVersion;
});
