import type { components } from '../api/schema'

export type Workspace = components['schemas']['Workspace']
export type WorkspaceProject = components['schemas']['WorkspaceProject']
export type WorkspaceForum = components['schemas']['WorkspaceForum']
export type WorkspaceForumCollaborator =
  components['schemas']['WorkspaceForumCollaborator']
export type DiscordMember = components['schemas']['DiscordMember']

export interface WorkspaceList {
  items: Workspace[]
}
