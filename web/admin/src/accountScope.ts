type Ticket = { epoch: number; lane: string; sequence: number };

/** Rejects late responses, including A -> B -> A and same-account chat changes. */
export class AccountScope {
  private epoch = 0;
  private sequences = new Map<string, number>();
  private device = "";
  private owner = "";
  private generation = 0;
  private chat = "";

  update(device: string, owner: string, generation: number) {
    if (device === this.device && owner === this.owner && generation === this.generation) return;
    this.device = device;
    this.owner = owner;
    this.generation = generation;
    this.epoch++;
    this.chat = "";
    this.sequences.clear();
  }

  selectChat(chat: string) {
    if (chat === this.chat) return;
    this.chat = chat;
    for (const lane of ["messages", "send"]) {
      this.sequences.set(lane, (this.sequences.get(lane) ?? 0) + 1);
    }
  }

  begin(lane: string, device: string, owner: string, chat?: string): Ticket | undefined {
    if (!owner || device !== this.device || owner !== this.owner || (chat !== undefined && chat !== this.chat)) return;
    const sequence = (this.sequences.get(lane) ?? 0) + 1;
    this.sequences.set(lane, sequence);
    return { epoch: this.epoch, lane, sequence };
  }

  matches(ticket: Ticket | undefined) {
    return !!ticket && ticket.epoch === this.epoch && ticket.sequence === this.sequences.get(ticket.lane);
  }
}

export function belongsToAccount(row: { device: string; owner_wxid?: string }, device: string, owner: string) {
  return !!owner && row.device === device && row.owner_wxid === owner;
}
