import { apiClient } from "@/lib/api-client";
import type { PlatformSkill } from "@/shared/api/types";

// The compiled platform skills every Run can load: read from the server so
// the app never names a skill the installed release does not ship.
class PlatformSkillService {
  async list(companyId: string): Promise<{ items: PlatformSkill[] }> {
    return apiClient.get<{ items: PlatformSkill[] }>(
      `/companies/${encodeURIComponent(companyId)}/platform-skills`,
    );
  }
}

export const platformSkillService = new PlatformSkillService();
