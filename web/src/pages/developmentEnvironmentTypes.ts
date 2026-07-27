import type { components } from '../api/schema'

export type DevelopmentEnvironment =
  components['schemas']['DevelopmentEnvironment']
export type DevelopmentProject = components['schemas']['DevelopmentProject']
export type DevelopmentForum = components['schemas']['DevelopmentForum']
export type DevelopmentForumCollaborator =
  components['schemas']['DevelopmentForumCollaborator']
export type DiscordMember = components['schemas']['DiscordMember']

export interface DevelopmentEnvironmentList {
  items: DevelopmentEnvironment[]
}
