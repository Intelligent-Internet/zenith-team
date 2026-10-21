import {
  type ZenithTeamProxyEnv,
  proxyZenithTeamRequest,
} from "../_shared/zenith-team-proxy";

type PagesFunctionContext = {
  env: ZenithTeamProxyEnv;
  request: Request;
};

export function onRequest(context: PagesFunctionContext): Promise<Response> {
  return proxyZenithTeamRequest(
    context.request,
    context.env.ZENITH_TEAM_API_ORIGIN,
    "Zenith Team API",
  );
}
