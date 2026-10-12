import {
  type BoardlyProxyEnv,
  proxyBoardlyRequest,
} from "../_shared/boardly-proxy";

type PagesFunctionContext = {
  env: BoardlyProxyEnv;
  request: Request;
};

export function onRequest(context: PagesFunctionContext): Promise<Response> {
  return proxyBoardlyRequest(
    context.request,
    context.env.BOARDLY_API_ORIGIN,
    "Boardly API",
  );
}
